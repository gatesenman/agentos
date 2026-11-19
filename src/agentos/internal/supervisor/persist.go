package supervisor

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/agentos/agentos/internal/api"
)

const specFile = "spec.json"

func (s *local) specPath(id string) string { return filepath.Join(s.agentDir(id), specFile) }

// saveSpec atomically writes DataDir/agents/<id>/spec.json.
func (s *local) saveSpec(spec api.AgentSpec) error {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	dir := s.agentDir(spec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".spec-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, s.specPath(spec.ID))
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// deleteSpec removes spec.json (the workspace is kept). Reports whether it existed.
func (s *local) deleteSpec(id string) (bool, error) {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	err := os.Remove(s.specPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// loadSpecs reads every DataDir/agents/*/spec.json. Broken files are skipped.
func (s *local) loadSpecs() []api.AgentSpec {
	ents, err := os.ReadDir(filepath.Join(s.opt.DataDir, "agents"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logf("cannot list agents dir: %v", err)
		}
		return nil
	}
	var out []api.AgentSpec
	for _, e := range ents {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		b, err := os.ReadFile(s.specPath(e.Name()))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				logf("cannot read spec of %s: %v", e.Name(), err)
			}
			continue
		}
		var spec api.AgentSpec
		if err := json.Unmarshal(b, &spec); err != nil || spec.ID != e.Name() || spec.Command == "" {
			logf("ignoring invalid spec %s (err=%v)", s.specPath(e.Name()), err)
			continue
		}
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// stopMarker records that the user explicitly stopped an agent, so AutoStart
// does not bring it back at node boot or on a server re-sync.
const stopMarker = "user-stopped"

func (s *local) hasStopMarker(id string) bool {
	_, err := os.Stat(filepath.Join(s.agentDir(id), stopMarker))
	return err == nil
}

func (s *local) setStopMarker(id string, on bool) {
	if !validID(id) {
		return
	}
	path := filepath.Join(s.agentDir(id), stopMarker)
	if on {
		os.MkdirAll(s.agentDir(id), 0o755)
		os.WriteFile(path, nil, 0o644)
	} else {
		os.Remove(path)
	}
}
