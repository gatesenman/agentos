package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
)

func openT(t *testing.T, path string) *sqliteStore {
	t.Helper()
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.(*sqliteStore)
}

func TestMigrateFromEmpty(t *testing.T) {
	s := openT(t, filepath.Join(t.TempDir(), "e.db"))
	v, err := s.GetSetting(context.Background(), SettingSchemaVersion)
	if err != nil || v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version=%q err=%v want %d", v, err, SchemaVersion)
	}
	for _, tbl := range []string{"settings", "nodes", "agents", "sessions"} {
		var n int
		s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n)
		if n != 1 {
			t.Fatalf("table %s missing", tbl)
		}
	}
}

// v1 = the exact phase-1 schema (commit 27cb50a), with data.
const v1Schema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, hostname TEXT, os TEXT, arch TEXT, version TEXT,
  remote_addr TEXT, capabilities TEXT, last_seen INTEGER, created_at INTEGER);
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY, node_id TEXT NOT NULL, name TEXT NOT NULL, spec TEXT NOT NULL,
  created_at INTEGER, updated_at INTEGER);
CREATE INDEX IF NOT EXISTS agents_node ON agents(node_id);
INSERT OR IGNORE INTO settings(key,value) VALUES ('schema_version','1');
INSERT INTO settings(key,value) VALUES ('node_token','aos_old');
INSERT INTO nodes(id,name,capabilities,last_seen,created_at) VALUES ('n1','box','["stats"]',0,0);
INSERT INTO agents(id,node_id,name,spec,created_at,updated_at) VALUES ('a1','n1','echo','{"id":"a1","node_id":"n1","name":"echo","command":"echo"}',0,0);
`

func TestMigrateFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(v1Schema); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s := openT(t, path)
	ctx := context.Background()
	if v, _ := s.GetSetting(ctx, SettingSchemaVersion); v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version=%s", v)
	}
	if v, _ := s.GetSetting(ctx, SettingNodeToken); v != "aos_old" {
		t.Fatalf("node token lost: %q", v)
	}
	if a, err := s.GetAgent(ctx, "a1"); err != nil || a.Command != "echo" {
		t.Fatalf("agent lost: %+v %v", a, err)
	}
	if n, err := s.GetNode(ctx, "n1"); err != nil || n.Name != "box" {
		t.Fatalf("node lost: %+v %v", n, err)
	}
	if err := s.CreateSession(ctx, Session{ID: "x", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("sessions table not created: %v", err)
	}
	// Re-open is a no-op.
	s.Close()
	s2 := openT(t, path)
	if _, err := s2.GetSession(ctx, "x"); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateTooNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	s := openT(t, path)
	s.SetSetting(context.Background(), SettingSchemaVersion, strconv.Itoa(SchemaVersion+1))
	s.Close()
	if _, err := OpenSQLite(path); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("want ErrSchemaTooNew, got %v", err)
	}
}

func TestNodesAllMethods(t *testing.T) {
	s := openT(t, filepath.Join(t.TempDir(), "n.db"))
	ctx := context.Background()
	if _, err := s.GetNode(ctx, "nope"); err != ErrNotFound {
		t.Fatal(err)
	}
	if err := s.DeleteNode(ctx, "nope"); err != ErrNotFound {
		t.Fatal(err)
	}
	seen := time.Now().Truncate(time.Millisecond)
	s.UpsertNode(ctx, api.Node{ID: "b", Name: "zeta", LastSeen: seen})
	s.UpsertNode(ctx, api.Node{ID: "a", Name: "alpha", OS: "linux", Status: api.NodeOnline})
	n, _ := s.GetNode(ctx, "a")
	created := n.CreatedAt
	if n.Status != api.NodeOffline || n.CreatedAt.IsZero() {
		t.Fatalf("%+v", n)
	}
	s.UpsertNode(ctx, api.Node{ID: "a", Name: "alpha2", OS: "windows"})
	n, _ = s.GetNode(ctx, "a")
	if n.Name != "alpha2" || n.OS != "windows" || !n.CreatedAt.Equal(created) {
		t.Fatalf("upsert update: %+v", n)
	}
	list, err := s.ListNodes(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "a" || !list[1].LastSeen.Equal(seen) {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := s.DeleteNode(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListNodes(ctx); len(list) != 1 {
		t.Fatal(list)
	}
}

func TestAgentsAllMethods(t *testing.T) {
	s := openT(t, filepath.Join(t.TempDir(), "a.db"))
	ctx := context.Background()
	s.CreateAgent(ctx, api.AgentSpec{ID: "1", NodeID: "n1", Name: "b"})
	s.CreateAgent(ctx, api.AgentSpec{ID: "2", NodeID: "n2", Name: "a"})
	if err := s.CreateAgent(ctx, api.AgentSpec{ID: "1", NodeID: "n1", Name: "dup"}); err == nil {
		t.Fatal("duplicate id accepted")
	}
	all, _ := s.ListAgents(ctx, "")
	if len(all) != 2 || all[0].ID != "2" {
		t.Fatalf("%+v", all)
	}
	n1, _ := s.ListAgents(ctx, "n1")
	if len(n1) != 1 || n1[0].ID != "1" {
		t.Fatalf("%+v", n1)
	}
	if err := s.UpdateAgent(ctx, api.AgentSpec{ID: "1", NodeID: "n2", Name: "moved"}); err != nil {
		t.Fatal(err)
	}
	if n2, _ := s.ListAgents(ctx, "n2"); len(n2) != 2 {
		t.Fatalf("%+v", n2)
	}
	if err := s.UpdateAgent(ctx, api.AgentSpec{ID: "zz"}); err != ErrNotFound {
		t.Fatal(err)
	}
	if err := s.DeleteAgent(ctx, "zz"); err != ErrNotFound {
		t.Fatal(err)
	}
	if empty, _ := s.ListAgents(ctx, "nobody"); empty == nil || len(empty) != 0 {
		t.Fatal("want empty non-nil slice")
	}
}

func TestSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	s := openT(t, path)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	if _, err := s.GetSession(ctx, "a"); err != ErrNotFound {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := s.CreateSession(ctx, Session{ID: id, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	s.CreateSession(ctx, Session{ID: "old", CreatedAt: now, ExpiresAt: now.Add(-time.Second)})
	x, err := s.GetSession(ctx, "a")
	if err != nil || !x.ExpiresAt.Equal(now.Add(time.Hour)) || !x.LastSeen.Equal(now) {
		t.Fatalf("%+v %v", x, err)
	}
	if err := s.TouchSession(ctx, "a", now.Add(time.Minute), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, "zz", now, now); err != ErrNotFound {
		t.Fatal(err)
	}
	if n, _ := s.PurgeExpiredSessions(ctx, now); n != 1 {
		t.Fatalf("purged %d", n)
	}
	if err := s.DeleteSession(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "b"); err != ErrNotFound {
		t.Fatal(err)
	}
	// persisted across reopen
	s.Close()
	s = openT(t, path)
	x, err = s.GetSession(ctx, "a")
	if err != nil || !x.ExpiresAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("after reopen %+v %v", x, err)
	}
	if n, _ := s.DeleteSessionsExcept(ctx, "a"); n != 1 {
		t.Fatalf("deleted %d", n)
	}
	if n, _ := s.DeleteSessionsExcept(ctx, ""); n != 1 {
		t.Fatalf("deleted %d", n)
	}
}

func TestConcurrentAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	s := openT(t, path)
	s2 := openT(t, path) // second handle on the same file (e.g. admin CLI)
	ctx := context.Background()
	var wg sync.WaitGroup
	errc := make(chan error, 1000)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			st := Store(s)
			if w%2 == 1 {
				st = s2
			}
			for i := 0; i < 25; i++ {
				id := fmt.Sprintf("w%d-%d", w, i)
				if err := st.CreateAgent(ctx, api.AgentSpec{ID: id, NodeID: "n", Name: id}); err != nil {
					errc <- err
				}
				if err := st.SetSetting(ctx, "k", id); err != nil {
					errc <- err
				}
				if _, err := st.ListAgents(ctx, "n"); err != nil {
					errc <- err
				}
				if err := st.(SessionStore).CreateSession(ctx, Session{ID: id, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
					errc <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
	if l, _ := s.ListAgents(ctx, ""); len(l) != 200 {
		t.Fatalf("got %d agents", len(l))
	}
}
