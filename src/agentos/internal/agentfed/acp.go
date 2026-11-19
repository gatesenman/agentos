package agentfed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ACPClient talks to a legacy Bee/IBM ACP server (REST OpenAPI 0.2.x).
// ACP has merged into A2A under LF; this client is best-effort for remaining
// ACP endpoints (GET /agents, POST /runs, SSE events).
type ACPClient struct {
	HTTP    *http.Client
	BaseURL string
	Auth    Auth
}

type ACPManifest struct {
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	InputContentTypes  []string        `json:"input_content_types"`
	OutputContentTypes []string        `json:"output_content_types"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	Status             json.RawMessage `json:"status,omitempty"`
	Raw                json.RawMessage `json:"-"`
}

type ACPAgentsList struct {
	Agents []ACPManifest `json:"agents"`
}

type ACPRun struct {
	AgentName string          `json:"agent_name"`
	RunID     string          `json:"run_id"`
	Status    string          `json:"status"`
	Output    json.RawMessage `json:"output"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	CreatedAt string `json:"created_at"`
}

// DiscoverACP lists agents at an ACP server root (GET /agents).
func DiscoverACP(ctx context.Context, base string, auth Auth, hc *http.Client) ([]ACPManifest, error) {
	if hc == nil {
		hc = defaultHTTP()
	}
	base = strings.TrimRight(base, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/agents", nil)
	if err != nil {
		return nil, err
	}
	applyAuth(req, auth)
	req.Header.Set("Accept", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("ACP GET /agents HTTP %d: %s", res.StatusCode, truncate(string(b), 200))
	}
	var list ACPAgentsList
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	return list.Agents, nil
}

// GetACPAgent fetches one manifest (GET /agents/{name}).
func GetACPAgent(ctx context.Context, base, name string, auth Auth, hc *http.Client) (ACPManifest, error) {
	if hc == nil {
		hc = defaultHTTP()
	}
	base = strings.TrimRight(base, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/agents/"+name, nil)
	if err != nil {
		return ACPManifest{}, err
	}
	applyAuth(req, auth)
	res, err := hc.Do(req)
	if err != nil {
		return ACPManifest{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		return ACPManifest{}, fmt.Errorf("ACP GET /agents/%s HTTP %d", name, res.StatusCode)
	}
	var m ACPManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return ACPManifest{}, err
	}
	m.Raw = append(json.RawMessage(nil), b...)
	return m, nil
}

func (c *ACPClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/ping", nil)
	if err != nil {
		return err
	}
	applyAuth(req, c.Auth)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("ACP ping HTTP %d", res.StatusCode)
	}
	return nil
}

// CreateRun starts an ACP run. If stream is true, Accept: text/event-stream.
func (c *ACPClient) CreateRun(ctx context.Context, agentName, text string, stream bool, onEvent func(string)) (ACPRun, error) {
	body, _ := json.Marshal(map[string]any{
		"agent_name": agentName,
		"input": []map[string]any{{
			"role": "user",
			"parts": []map[string]any{{
				"content_type": "text/plain",
				"content":      text,
			}},
		}},
		"mode": map[bool]string{true: "stream", false: "sync"}[stream],
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/runs", bytes.NewReader(body))
	if err != nil {
		return ACPRun{}, err
	}
	applyAuth(req, c.Auth)
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream, application/json")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return ACPRun{}, err
	}
	defer res.Body.Close()
	ct := res.Header.Get("Content-Type")
	if stream && strings.Contains(ct, "event-stream") {
		run := ACPRun{AgentName: agentName, Status: "in-progress"}
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				continue
			}
			if onEvent != nil {
				onEvent(data)
			}
			var ev map[string]any
			if json.Unmarshal([]byte(data), &ev) == nil {
				if r, ok := ev["run"].(map[string]any); ok {
					if id, _ := r["run_id"].(string); id != "" {
						run.RunID = id
					}
					if st, _ := r["status"].(string); st != "" {
						run.Status = st
					}
				}
				if t, _ := ev["type"].(string); t == "message.part" {
					if p, ok := ev["part"].(map[string]any); ok {
						if content, _ := p["content"].(string); content != "" && onEvent != nil {
							onEvent(content)
						}
					}
				}
			}
		}
		return run, sc.Err()
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode >= 300 {
		return ACPRun{}, fmt.Errorf("ACP POST /runs HTTP %d: %s", res.StatusCode, truncate(string(b), 200))
	}
	var run ACPRun
	if err := json.Unmarshal(b, &run); err != nil {
		return ACPRun{}, err
	}
	// Servers may answer sync requests early (created/in-progress): poll to a terminal state.
	for i := 0; i < 600 && run.RunID != "" && !acpTerminal(run.Status); i++ {
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		if r2, err := c.GetRun(ctx, run.RunID); err == nil {
			run = r2
		}
	}
	if run.Status == "failed" && run.Error != nil {
		return run, fmt.Errorf("ACP run failed: %s %s", run.Error.Code, truncate(run.Error.Message, 200))
	}
	if onEvent != nil {
		onEvent(fmt.Sprintf("run %s status=%s", run.RunID, run.Status))
		if t := extractACPOutput(run.Output); t != "" {
			onEvent(t)
		}
	}
	return run, nil
}

func (c *ACPClient) GetRun(ctx context.Context, runID string) (ACPRun, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/runs/"+runID, nil)
	if err != nil {
		return ACPRun{}, err
	}
	applyAuth(req, c.Auth)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return ACPRun{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		return ACPRun{}, fmt.Errorf("ACP GET /runs/%s HTTP %d", runID, res.StatusCode)
	}
	var run ACPRun
	return run, json.Unmarshal(b, &run)
}

func (c *ACPClient) CancelRun(ctx context.Context, runID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/runs/"+runID+"/cancel", nil)
	if err != nil {
		return err
	}
	applyAuth(req, c.Auth)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode != 202 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return fmt.Errorf("ACP cancel HTTP %d: %s", res.StatusCode, truncate(string(b), 200))
	}
	return nil
}

func extractACPOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var msgs []struct {
		Parts []struct {
			Content string `json:"content"`
		} `json:"parts"`
	}
	if json.Unmarshal(raw, &msgs) == nil {
		var b strings.Builder
		for _, m := range msgs {
			for _, p := range m.Parts {
				if p.Content != "" {
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					b.WriteString(p.Content)
				}
			}
		}
		return b.String()
	}
	return ""
}

func NormalizeACPState(s string) string {
	switch strings.ToLower(s) {
	case "created", "in-progress":
		return "running"
	case "awaiting":
		return "awaiting"
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled", "canceling", "cancelling":
		return "cancelled"
	default:
		if s == "" {
			return "unknown"
		}
		return s
	}
}

// CapListFromACP builds capability strings from a manifest.
func CapListFromACP(m ACPManifest) []string {
	out := []string{"acp"}
	for _, t := range m.InputContentTypes {
		out = append(out, "in:"+t)
	}
	for _, t := range m.OutputContentTypes {
		out = append(out, "out:"+t)
	}
	return out
}

// DetectProtocol probes endpoint: A2A card first, then ACP /agents.
func DetectProtocol(ctx context.Context, endpoint string, auth Auth, hc *http.Client) (ProbeResult, error) {
	if hc == nil {
		hc = defaultHTTP()
	}
	// A2A 1.0 well-known card
	if card, err := FetchAgentCard(ctx, endpoint, auth, hc); err == nil && card.Name != "" {
		rpc, _, _ := card.ResolveInterface()
		ep := rpc
		if ep == "" {
			ep = endpoint
		}
		return ProbeResult{
			Protocol: ProtoA2A, Name: card.Name, Description: card.Description,
			Endpoint: ep, Capabilities: card.CapList(), Card: card.Raw, OK: true,
		}, nil
	}
	// ACP list
	if agents, err := DiscoverACP(ctx, endpoint, auth, hc); err == nil && len(agents) > 0 {
		a := agents[0]
		raw, _ := json.Marshal(agents)
		caps := CapListFromACP(a)
		return ProbeResult{
			Protocol: ProtoACP, Name: a.Name, Description: a.Description,
			Endpoint: strings.TrimRight(endpoint, "/"), Capabilities: caps, Card: raw, OK: true,
		}, nil
	}
	// ACP single agent URL .../agents/{name}
	if i := strings.LastIndex(endpoint, "/agents/"); i >= 0 {
		base := endpoint[:i]
		name := strings.Trim(endpoint[i+len("/agents/"):], "/")
		if m, err := GetACPAgent(ctx, base, name, auth, hc); err == nil {
			return ProbeResult{
				Protocol: ProtoACP, Name: m.Name, Description: m.Description,
				Endpoint: base, Capabilities: CapListFromACP(m), Card: m.Raw, OK: true,
			}, nil
		}
	}
	return ProbeResult{OK: false, Error: "neither A2A Agent Card nor ACP /agents found", Endpoint: endpoint}, nil
}

// Ensure time import used
var _ = time.Second

func acpTerminal(st string) bool {
	switch st {
	case "completed", "failed", "cancelled", "awaiting":
		return true
	}
	return false
}
