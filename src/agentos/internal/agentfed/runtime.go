package agentfed

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
)

// LogLine is a federation log event (mirrors api.LogLine shape for UI reuse).
type LogLine struct {
	AgentID string    `json:"agent_id"`
	Seq     uint64    `json:"seq"`
	Time    time.Time `json:"time"`
	Stream  string    `json:"stream"` // stdout|system
	Text    string    `json:"text"`
}

// Runtime tracks in-memory tasks and log rings for federated agents.
type Runtime struct {
	mu     sync.Mutex
	tasks  map[string]*TaskInfo   // task id -> info
	byAg   map[string][]string    // agent id -> task ids
	logs   map[string][]LogLine   // agent id -> lines
	seq    map[string]uint64
	active map[string]string      // agent id -> current remote task/run id
	cancel map[string]context.CancelFunc
}

func NewRuntime() *Runtime {
	return &Runtime{
		tasks: map[string]*TaskInfo{}, byAg: map[string][]string{},
		logs: map[string][]LogLine{}, seq: map[string]uint64{},
		active: map[string]string{}, cancel: map[string]context.CancelFunc{},
	}
}

func (rt *Runtime) AppendLog(agentID, stream, text string) LogLine {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.seq[agentID]++
	ln := LogLine{AgentID: agentID, Seq: rt.seq[agentID], Time: time.Now().UTC(), Stream: stream, Text: text}
	rt.logs[agentID] = append(rt.logs[agentID], ln)
	if len(rt.logs[agentID]) > 2000 {
		rt.logs[agentID] = rt.logs[agentID][len(rt.logs[agentID])-2000:]
	}
	return ln
}

func (rt *Runtime) Logs(agentID string, tail int) []LogLine {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	all := rt.logs[agentID]
	if tail <= 0 || tail >= len(all) {
		out := make([]LogLine, len(all))
		copy(out, all)
		return out
	}
	out := make([]LogLine, tail)
	copy(out, all[len(all)-tail:])
	return out
}

func (rt *Runtime) ActiveRemote(agentID string) string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.active[agentID]
}

func (rt *Runtime) SetActive(agentID, remoteID string, cancel context.CancelFunc) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if old, ok := rt.cancel[agentID]; ok && old != nil {
		old()
	}
	rt.active[agentID] = remoteID
	rt.cancel[agentID] = cancel
}

func (rt *Runtime) ClearActive(agentID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if c, ok := rt.cancel[agentID]; ok && c != nil {
		c()
	}
	delete(rt.active, agentID)
	delete(rt.cancel, agentID)
}

func (rt *Runtime) Stop(agentID string) {
	rt.ClearActive(agentID)
	rt.AppendLog(agentID, "system", "stopped")
}

func (rt *Runtime) AddTask(agentID, protocol, remoteID, state, input string) *TaskInfo {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := time.Now().UTC()
	t := &TaskInfo{
		ID: uuid.NewString(), AgentID: agentID, Protocol: protocol, RemoteID: remoteID,
		State: state, Input: input, CreatedAt: now, UpdatedAt: now,
	}
	rt.tasks[t.ID] = t
	rt.byAg[agentID] = append(rt.byAg[agentID], t.ID)
	rt.active[agentID] = remoteID
	return t
}

func (rt *Runtime) UpdateTaskRemote(agentID, remoteID, state string, errMsg string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, id := range rt.byAg[agentID] {
		t := rt.tasks[id]
		if t != nil && t.RemoteID == remoteID {
			t.State = state
			t.UpdatedAt = time.Now().UTC()
			t.Error = errMsg
			if state == "completed" || state == "failed" || state == "cancelled" {
				now := time.Now().UTC()
				t.EndedAt = &now
			}
			return
		}
	}
}

func (rt *Runtime) ListTasks(agentID string) []TaskInfo {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	ids := rt.byAg[agentID]
	out := make([]TaskInfo, 0, len(ids))
	for _, id := range ids {
		if t := rt.tasks[id]; t != nil {
			out = append(out, *t)
		}
	}
	return out
}

func (rt *Runtime) AgentBusy(agentID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.active[agentID] != ""
}

// PublicCard strips nothing — card is already public metadata.
func PublicCard(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
