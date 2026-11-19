package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
)

func TestSQLiteCRUD(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.UpsertNode(ctx, api.Node{ID: "n1", Name: "box", Capabilities: []string{"stats"}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	n, err := s.GetNode(ctx, "n1")
	if err != nil || n.Name != "box" || len(n.Capabilities) != 1 {
		t.Fatalf("node %+v %v", n, err)
	}
	a := api.AgentSpec{ID: "a1", NodeID: "n1", Name: "echo", Command: "echo", Args: []string{"hi"}}
	if err := s.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Name = "echo2"
	if err := s.UpdateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListAgents(ctx, "n1")
	if len(list) != 1 || list[0].Name != "echo2" || list[0].Args[0] != "hi" {
		t.Fatalf("list %+v", list)
	}
	if err := s.DeleteAgent(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgent(ctx, "a1"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.GetSetting(ctx, "x"); err != ErrNotFound {
		t.Fatal(err)
	}
	s.SetSetting(ctx, "x", "1")
	if v, _ := s.GetSetting(ctx, "x"); v != "1" {
		t.Fatal(v)
	}
}
