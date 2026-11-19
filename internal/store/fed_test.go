package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFedStore(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fs := st.(FedStore)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := FedRow{
		ID: "f1", Name: "Echo", Protocol: "a2a", Endpoint: "http://127.0.0.1:9",
		AuthScheme: "bearer", AuthToken: "fed1:x", Status: "unknown",
		Capabilities: []string{"streaming"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := fs.CreateFedAgent(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := fs.GetFedAgent(ctx, "f1")
	if err != nil || got.Name != "Echo" || got.Protocol != "a2a" || len(got.Capabilities) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	list, _ := fs.ListFedAgents(ctx, "a2a")
	if len(list) != 1 {
		t.Fatal(list)
	}
	row.Status = "online"
	row.UpdatedAt = now.Add(time.Second)
	if err := fs.UpdateFedAgent(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := fs.DeleteFedAgent(ctx, "f1"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.GetFedAgent(ctx, "f1"); err != ErrNotFound {
		t.Fatal(err)
	}
}
