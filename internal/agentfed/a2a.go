package agentfed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A2AClient talks to a remote A2A agent via JSON-RPC 2.0 (preferred) or HTTP+JSON REST.
// Compatible with Agent Card v0.3 (url/preferredTransport) and v1.0 (supportedInterfaces).
type A2AClient struct {
	HTTP    *http.Client
	BaseURL string // card or RPC endpoint base
	RPCURL  string // resolved JSON-RPC / REST base
	Binding string // JSONRPC | HTTP+JSON
	Version string // 0.3 | 1.0
	Auth    Auth
	Card    AgentCard
}

// AgentCard is a minimal decode of public Agent Card fields (v0.3 + v1.0).
type AgentCard struct {
	Name                 string           `json:"name"`
	Description          string           `json:"description"`
	URL                  string           `json:"url"` // v0.3
	PreferredTransport   string           `json:"preferredTransport"`
	AdditionalInterfaces []AgentInterface `json:"additionalInterfaces"`
	SupportedInterfaces  []AgentInterface `json:"supportedInterfaces"` // v1.0
	Capabilities         CardCaps         `json:"capabilities"`
	DefaultInputModes    []string         `json:"defaultInputModes"`
	DefaultOutputModes   []string         `json:"defaultOutputModes"`
	Skills               []AgentSkill     `json:"skills"`
	Version              string           `json:"version"`
	ProtocolVersion      string           `json:"protocolVersion"` // v0.3 top-level
	Raw                  json.RawMessage  `json:"-"`
}

type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"` // JSONRPC | HTTP+JSON | GRPC
	Transport       string `json:"transport"`       // v0.3 alias
	ProtocolVersion string `json:"protocolVersion"`
}

type CardCaps struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
	ExtendedAgentCard bool `json:"extendedAgentCard"`
}

type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

func defaultHTTP() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

// FetchAgentCard retrieves /.well-known/agent-card.json (and legacy agent.json).
func FetchAgentCard(ctx context.Context, endpoint string, auth Auth, hc *http.Client) (AgentCard, error) {
	if hc == nil {
		hc = defaultHTTP()
	}
	base := strings.TrimRight(endpoint, "/")
	candidates := []string{
		base,
		base + "/.well-known/agent-card.json",
		base + "/.well-known/agent.json",
		base + "/agent-card.json",
	}
	// If caller already pointed at the card file, try it first only.
	if strings.Contains(endpoint, "agent-card") || strings.HasSuffix(endpoint, ".json") {
		candidates = []string{endpoint, base}
	}
	var last error
	for _, u := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			last = err
			continue
		}
		applyAuth(req, auth)
		req.Header.Set("Accept", "application/json")
		res, err := hc.Do(req)
		if err != nil {
			last = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode != 200 {
			last = fmt.Errorf("GET %s: HTTP %d", u, res.StatusCode)
			continue
		}
		var card AgentCard
		if err := json.Unmarshal(body, &card); err != nil {
			last = err
			continue
		}
		// Heuristic: must look like a card.
		if card.Name == "" && card.URL == "" && len(card.SupportedInterfaces) == 0 && len(card.Skills) == 0 {
			last = fmt.Errorf("%s: not an agent card", u)
			continue
		}
		card.Raw = append(json.RawMessage(nil), body...)
		return card, nil
	}
	if last == nil {
		last = fmt.Errorf("agent card not found at %s", endpoint)
	}
	return AgentCard{}, last
}

func (c *AgentCard) CapList() []string {
	var out []string
	if c.Capabilities.Streaming {
		out = append(out, "streaming")
	}
	if c.Capabilities.PushNotifications {
		out = append(out, "pushNotifications")
	}
	for _, s := range c.Skills {
		if s.Name != "" {
			out = append(out, "skill:"+s.Name)
		} else if s.ID != "" {
			out = append(out, "skill:"+s.ID)
		}
	}
	return out
}

func (c *AgentCard) ResolveInterface() (rpcURL, binding, version string) {
	for _, iface := range c.SupportedInterfaces {
		b := strings.ToUpper(iface.ProtocolBinding)
		if b == "" {
			b = strings.ToUpper(iface.Transport)
		}
		if b == "JSONRPC" || b == "HTTP+JSON" || b == "HTTP+JSON/REST" {
			v := iface.ProtocolVersion
			if v == "" {
				v = "1.0"
			}
			return iface.URL, b, v
		}
	}
	// v0.3
	if c.URL != "" {
		b := strings.ToUpper(c.PreferredTransport)
		if b == "" {
			b = "JSONRPC"
		}
		v := c.ProtocolVersion
		if v == "" {
			v = "0.3"
		}
		return c.URL, b, v
	}
	for _, iface := range c.AdditionalInterfaces {
		b := strings.ToUpper(iface.Transport)
		if b == "" {
			b = strings.ToUpper(iface.ProtocolBinding)
		}
		if b != "" {
			return iface.URL, b, "0.3"
		}
	}
	return "", "", ""
}

// NewA2AClientFromEndpoint fetches the card and builds a client.
func NewA2AClientFromEndpoint(ctx context.Context, endpoint string, auth Auth, hc *http.Client) (*A2AClient, error) {
	card, err := FetchAgentCard(ctx, endpoint, auth, hc)
	if err != nil {
		return nil, err
	}
	rpc, binding, ver := card.ResolveInterface()
	if rpc == "" {
		// Fall back to endpoint as RPC root.
		rpc = strings.TrimRight(endpoint, "/")
		binding = "JSONRPC"
		ver = "1.0"
	}
	if hc == nil {
		hc = defaultHTTP()
	}
	return &A2AClient{HTTP: hc, BaseURL: endpoint, RPCURL: rpc, Binding: binding, Version: ver, Auth: auth, Card: card}, nil
}

type a2aMsgPart struct {
	Type string `json:"type,omitempty"` // text (0.3)
	Text string `json:"text,omitempty"`
}

type a2aMessage struct {
	MessageID string       `json:"messageId"`
	Role      string       `json:"role"` // user | ROLE_USER
	Parts     []a2aMsgPart `json:"parts"`
}

type jsonRPCReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type jsonRPCResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// SendMessage creates a task (or returns a message) via JSON-RPC or REST.
func (c *A2AClient) SendMessage(ctx context.Context, text string) (taskID string, state string, raw json.RawMessage, err error) {
	msgID := fmt.Sprintf("msg-%d", time.Now().UnixNano())
	role := "user"
	if strings.HasPrefix(c.Version, "1.") {
		role = "ROLE_USER"
	}
	msg := a2aMessage{MessageID: msgID, Role: role, Parts: []a2aMsgPart{{Type: "text", Text: text}}}
	params := map[string]any{
		"message": msg,
		"configuration": map[string]any{
			"returnImmediately": true,
		},
	}

	if strings.Contains(c.Binding, "HTTP") && !strings.Contains(c.Binding, "JSONRPC") {
		return c.sendREST(ctx, text, msg)
	}

	// Try v1 method names then legacy message/send.
	methods := []string{"SendMessage", "message/send"}
	var last error
	for _, m := range methods {
		raw, err := c.rpc(ctx, m, params)
		if err != nil {
			last = err
			continue
		}
		taskID, state = parseTaskResult(raw)
		return taskID, state, raw, nil
	}
	return "", "", nil, last
}

func (c *A2AClient) sendREST(ctx context.Context, text string, msg a2aMessage) (string, string, json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"message": msg, "configuration": map[string]any{"returnImmediately": true}})
	u := strings.TrimRight(c.RPCURL, "/") + "/message:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", "", nil, err
	}
	applyAuth(req, c.Auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", c.Version)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode >= 300 {
		return "", "", nil, fmt.Errorf("REST message:send HTTP %d: %s", res.StatusCode, truncate(string(b), 200))
	}
	id, st := parseTaskResult(b)
	return id, st, b, nil
}

func (c *A2AClient) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	payload, _ := json.Marshal(jsonRPCReq{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RPCURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	applyAuth(req, c.Auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", c.Version)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("JSON-RPC %s HTTP %d: %s", method, res.StatusCode, truncate(string(b), 200))
	}
	var resp jsonRPCResp
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("JSON-RPC %s: %s (%d)", method, resp.Error.Message, resp.Error.Code)
	}
	return resp.Result, nil
}

func parseTaskResult(raw json.RawMessage) (id, state string) {
	var wrap struct {
		Task *struct {
			ID     string `json:"id"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"task"`
		ID     string `json:"id"`
		Status struct {
			State string `json:"state"`
		} `json:"status"`
		Message *struct {
			Parts []a2aMsgPart `json:"parts"`
		} `json:"message"`
	}
	_ = json.Unmarshal(raw, &wrap)
	if wrap.Task != nil {
		return wrap.Task.ID, normalizeA2AState(wrap.Task.Status.State)
	}
	if wrap.ID != "" {
		return wrap.ID, normalizeA2AState(wrap.Status.State)
	}
	if wrap.Message != nil {
		return "", "completed"
	}
	return "", "unknown"
}

func normalizeA2AState(s string) string {
	s = strings.TrimPrefix(strings.ToLower(s), "task_state_")
	s = strings.ReplaceAll(s, "-", "_")
	switch s {
	case "submitted", "working":
		return "running"
	case "completed":
		return "completed"
	case "failed", "rejected":
		return "failed"
	case "canceled", "cancelled":
		return "cancelled"
	case "input_required", "auth_required":
		return "awaiting"
	default:
		if s == "" {
			return "unknown"
		}
		return s
	}
}

// GetTask polls task state.
func (c *A2AClient) GetTask(ctx context.Context, taskID string) (state string, raw json.RawMessage, err error) {
	raw, err = c.rpc(ctx, "GetTask", map[string]any{"id": taskID})
	if err != nil {
		raw, err = c.rpc(ctx, "tasks/get", map[string]any{"id": taskID})
	}
	if err != nil {
		return "", nil, err
	}
	_, state = parseTaskResult(raw)
	if state == "unknown" {
		_, state = parseTaskResult(mustWrapTask(raw))
	}
	return state, raw, nil
}

func mustWrapTask(raw json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteString(`{"task":`)
	buf.Write(raw)
	buf.WriteByte('}')
	return buf.Bytes()
}

// CancelTask asks the remote agent to cancel.
func (c *A2AClient) CancelTask(ctx context.Context, taskID string) error {
	_, err := c.rpc(ctx, "CancelTask", map[string]any{"id": taskID})
	if err != nil {
		_, err = c.rpc(ctx, "tasks/cancel", map[string]any{"id": taskID})
	}
	return err
}

// StreamMessage sends a message and yields SSE / JSON-RPC stream lines as text events.
func (c *A2AClient) StreamMessage(ctx context.Context, text string, onEvent func(string)) (taskID string, err error) {
	if !c.Card.Capabilities.Streaming {
		id, st, raw, err := c.SendMessage(ctx, text)
		if err != nil {
			return "", err
		}
		onEvent(fmt.Sprintf("task %s state=%s", id, st))
		onEvent(extractTextFromResult(raw))
		return id, nil
	}
	msgID := fmt.Sprintf("msg-%d", time.Now().UnixNano())
	role := "user"
	if strings.HasPrefix(c.Version, "1.") {
		role = "ROLE_USER"
	}
	params := map[string]any{
		"message": a2aMessage{MessageID: msgID, Role: role, Parts: []a2aMsgPart{{Type: "text", Text: text}}},
	}
	payload, _ := json.Marshal(jsonRPCReq{JSONRPC: "2.0", ID: msgID, Method: "SendStreamingMessage", Params: params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RPCURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	applyAuth(req, c.Auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	if c.Version != "" {
		req.Header.Set("A2A-Version", c.Version) // 1.0 servers reject requests without it (default 0.3)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return "", fmt.Errorf("stream HTTP %d: %s", res.StatusCode, truncate(string(b), 200))
	}
	ct := res.Header.Get("Content-Type")
	if strings.Contains(ct, "event-stream") {
		return readSSE(res.Body, onEvent)
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	var resp jsonRPCResp
	if json.Unmarshal(b, &resp) == nil && resp.Error != nil {
		return "", fmt.Errorf("A2A error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if json.Unmarshal(b, &resp) == nil && resp.Result != nil {
		id, st := parseTaskResult(resp.Result)
		onEvent(fmt.Sprintf("task %s state=%s", id, st))
		onEvent(extractTextFromResult(resp.Result))
		return id, nil
	}
	onEvent(string(b))
	return "", nil
}

func readSSE(r io.Reader, onEvent func(string)) (taskID string, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		onEvent(data)
		var ev jsonRPCResp
		if json.Unmarshal([]byte(data), &ev) == nil && ev.Error != nil {
			return taskID, fmt.Errorf("A2A error %d: %s", ev.Error.Code, ev.Error.Message)
		}
		if ev.Result != nil {
			data = string(ev.Result)
		}
		if id, _ := parseTaskResult(json.RawMessage(data)); id != "" && taskID == "" {
			taskID = id
		}
		// Also try nested statusUpdate / artifactUpdate text.
		if t := extractTextFromResult(json.RawMessage(data)); t != "" && t != data {
			onEvent(t)
		}
	}
	return taskID, sc.Err()
}

func extractTextFromResult(raw json.RawMessage) string {
	var walk func(v any) string
	walk = func(v any) string {
		switch x := v.(type) {
		case map[string]any:
			if t, ok := x["text"].(string); ok && t != "" {
				return t
			}
			for _, k := range []string{"message", "artifact", "statusUpdate", "artifactUpdate", "status", "parts"} {
				if sub, ok := x[k]; ok {
					if s := walk(sub); s != "" {
						return s
					}
				}
			}
			for _, sub := range x {
				if s := walk(sub); s != "" {
					return s
				}
			}
		case []any:
			var b strings.Builder
			for _, el := range x {
				if s := walk(el); s != "" {
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					b.WriteString(s)
				}
			}
			return b.String()
		}
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return walk(v)
}

func applyAuth(req *http.Request, auth Auth) {
	if auth.Token == "" || auth.Scheme == AuthNone || auth.Scheme == "" {
		return
	}
	switch auth.Scheme {
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+auth.Token)
	case AuthAPIKey:
		h := auth.Header
		if h == "" {
			h = "Authorization"
		}
		if strings.EqualFold(h, "Authorization") && !strings.HasPrefix(auth.Token, "Bearer ") {
			req.Header.Set(h, "Bearer "+auth.Token)
		} else {
			req.Header.Set(h, auth.Token)
		}
	default:
		req.Header.Set("Authorization", "Bearer "+auth.Token)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// JoinURL resolves ref against base.
func JoinURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}
