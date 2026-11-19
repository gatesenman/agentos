package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/env"
)

func TestEnvStore(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	es := st.(EnvStore)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := EnvRow{Record: env.Record{ID: "e1", Name: "A", Provider: "agentos-local", Config: json.RawMessage(`{"x":1}`),
		Labels: map[string]string{"k": "v"}, State: env.StateCreating, CreatedAt: now, UpdatedAt: now}, Secrets: "sealed"}
	if err := es.CreateEnv(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := es.GetEnv(ctx, "e1")
	if err != nil || got.Name != "A" || got.Secrets != "sealed" || got.Labels["k"] != "v" || string(got.Config) != `{"x":1}` || !got.CreatedAt.Equal(now) {
		t.Fatalf("%+v %v", got, err)
	}
	got.NodeID, got.State = "n1", env.StateRunning
	if err := es.UpdateEnv(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, _ := es.ListEnvs(ctx)
	if len(list) != 1 || list[0].NodeID != "n1" || list[0].State != env.StateRunning {
		t.Fatalf("%+v", list)
	}
	if err := es.PutNodeCredential(ctx, NodeCredential{EnvID: "e1", TokenHash: "h1"}); err != nil {
		t.Fatal(err)
	}
	if err := es.BindNodeCredential(ctx, "e1", "nodeA"); err != nil {
		t.Fatal(err)
	}
	if err := es.BindNodeCredential(ctx, "e1", "nodeB"); err != ErrNotFound {
		t.Fatalf("rebind to another node must fail, got %v", err)
	}
	c, err := es.GetNodeCredentialByHash(ctx, "h1")
	if err != nil || c.NodeID != "nodeA" {
		t.Fatalf("%+v %v", c, err)
	}
	es.RevokeNode(ctx, "nodeA")
	if r, _ := es.NodeRevoked(ctx, "nodeA"); !r {
		t.Fatal("revoked")
	}
	if err := es.DeleteEnv(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	if _, err := es.GetEnv(ctx, "e1"); err != ErrNotFound {
		t.Fatal(err)
	}
}
