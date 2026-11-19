package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// FedRow is a persisted federated (A2A/ACP) agent. AuthToken is AES-GCM sealed;
// never returned to clients in plain form.
type FedRow struct {
	ID           string
	Name         string
	Description  string
	Protocol     string // a2a | acp
	Endpoint     string
	AuthScheme   string
	AuthHeader   string
	AuthToken    string // sealed
	AgentCard    json.RawMessage
	Capabilities []string
	Metadata     map[string]string
	Status       string
	LastError    string
	LastSeen     time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// FedStore is an optional SQLite extension (type-assert from Store).
type FedStore interface {
	ListFedAgents(ctx context.Context, protocol string) ([]FedRow, error) // protocol "" = all
	GetFedAgent(ctx context.Context, id string) (FedRow, error)
	CreateFedAgent(ctx context.Context, r FedRow) error
	UpdateFedAgent(ctx context.Context, r FedRow) error
	DeleteFedAgent(ctx context.Context, id string) error
}

var _ FedStore = (*sqliteStore)(nil)

const fedCols = `id,name,description,protocol,endpoint,auth_scheme,auth_header,auth_token,agent_card,capabilities,metadata,status,last_error,last_seen,created_at,updated_at`

func scanFed(row interface{ Scan(...any) error }) (FedRow, error) {
	var r FedRow
	var card, caps, meta string
	var lastSeen, created, updated int64
	err := row.Scan(&r.ID, &r.Name, &r.Description, &r.Protocol, &r.Endpoint, &r.AuthScheme, &r.AuthHeader, &r.AuthToken,
		&card, &caps, &meta, &r.Status, &r.LastError, &lastSeen, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if card != "" {
		r.AgentCard = json.RawMessage(card)
	}
	if caps != "" && caps != "[]" {
		_ = json.Unmarshal([]byte(caps), &r.Capabilities)
	}
	if meta != "" && meta != "{}" {
		_ = json.Unmarshal([]byte(meta), &r.Metadata)
	}
	if lastSeen > 0 {
		r.LastSeen = fromMs(lastSeen)
	}
	r.CreatedAt, r.UpdatedAt = fromMs(created), fromMs(updated)
	return r, nil
}

func fedArgs(r FedRow) []any {
	card := string(r.AgentCard)
	if card == "" {
		card = "null"
	}
	caps, _ := json.Marshal(r.Capabilities)
	if r.Capabilities == nil {
		caps = []byte("[]")
	}
	meta, _ := json.Marshal(r.Metadata)
	if r.Metadata == nil {
		meta = []byte("{}")
	}
	var ls int64
	if !r.LastSeen.IsZero() {
		ls = ms(r.LastSeen)
	}
	return []any{r.ID, r.Name, r.Description, r.Protocol, r.Endpoint, r.AuthScheme, r.AuthHeader, r.AuthToken,
		card, string(caps), string(meta), r.Status, r.LastError, ls, ms(r.CreatedAt), ms(r.UpdatedAt)}
}

func (s *sqliteStore) ListFedAgents(ctx context.Context, protocol string) ([]FedRow, error) {
	var rows *sql.Rows
	var err error
	if protocol == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT `+fedCols+` FROM fed_agents ORDER BY name COLLATE NOCASE`)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT `+fedCols+` FROM fed_agents WHERE protocol=? ORDER BY name COLLATE NOCASE`, protocol)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FedRow{}
	for rows.Next() {
		r, err := scanFed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqliteStore) GetFedAgent(ctx context.Context, id string) (FedRow, error) {
	return scanFed(s.db.QueryRowContext(ctx, `SELECT `+fedCols+` FROM fed_agents WHERE id=?`, id))
}

func (s *sqliteStore) CreateFedAgent(ctx context.Context, r FedRow) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO fed_agents (`+fedCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, fedArgs(r)...)
	return err
}

func (s *sqliteStore) UpdateFedAgent(ctx context.Context, r FedRow) error {
	res, err := s.db.ExecContext(ctx, `UPDATE fed_agents SET name=?,description=?,protocol=?,endpoint=?,auth_scheme=?,auth_header=?,auth_token=?,agent_card=?,capabilities=?,metadata=?,status=?,last_error=?,last_seen=?,updated_at=? WHERE id=?`,
		r.Name, r.Description, r.Protocol, r.Endpoint, r.AuthScheme, r.AuthHeader, r.AuthToken, string(r.AgentCard),
		mustJSON(r.Capabilities), mustJSON(r.Metadata), r.Status, r.LastError, msOr0(r.LastSeen), ms(r.UpdatedAt), r.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqliteStore) DeleteFedAgent(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM fed_agents WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	if len(b) == 0 || string(b) == "null" {
		switch v.(type) {
		case []string, []any:
			return "[]"
		default:
			return "{}"
		}
	}
	return string(b)
}

func msOr0(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}
