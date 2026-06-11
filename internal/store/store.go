package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Source string

const (
	SourceClaude Source = "claude"
	SourceCodex  Source = "codex"
)

type Kind string

const (
	KindSkill   Kind = "skill"
	KindCommand Kind = "command"
	KindMCP     Kind = "mcp"
)

type Event struct {
	ID                  int64
	Timestamp           time.Time
	Source              Source
	Kind                Kind
	Name                string
	SessionID           string
	Cwd                 string
	Host                string
	User                string
	Raw                 string
	ToolUseID           string
	DurationMs          int64
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

// Usage carries the per-turn token counts plus the model that produced them —
// both come from the same transcript entry, so they travel together through
// the finalize paths.
type Usage struct {
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

type Ranking struct {
	Name             string
	Count            int64
	AvgDurationMs    float64
	AvgInputTokens   float64
	AvgOutputTokens  float64
	AvgContextTokens float64
}

type DailyPoint struct {
	Day   string
	Count int64
}

// NameUsage is per-name usage used by the audit command to join recorded
// activity against the installed inventory. LastUsed is the most recent event
// timestamp for that name (zero if somehow unparsable).
type NameUsage struct {
	Name     string
	Count    int64
	LastUsed time.Time
}

type ProjectStat struct {
	Cwd   string
	Count int64
}

type HostStat struct {
	Host  string
	Count int64
}

type UserStat struct {
	User  string
	Count int64
}

// ModelStat counts events per model. Model is filled at finalize time from the
// session transcript, so rows that were never finalized group under "".
type ModelStat struct {
	Model string
	Count int64
}

type Store struct {
	db   *sql.DB
	sync func() error
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Sync() error {
	if s.sync == nil {
		return nil
	}
	return s.sync()
}

func (s *Store) Migrate(ctx context.Context) error {
	// Run all DDL in a single transaction. Turso's embedded replica pushes a
	// transaction's WAL frames atomically, so wrapping avoids the
	// "WAL frame insert conflict" we'd hit when each Exec ran as its own
	// implicit write transaction. SQLite has no "ADD COLUMN IF NOT EXISTS",
	// so we read PRAGMA table_info first and skip ALTERs whose columns
	// already exist (instead of catching a "duplicate column" error, which
	// would abort the surrounding tx).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS events (
	id                    INTEGER PRIMARY KEY AUTOINCREMENT,
	ts                    TEXT NOT NULL,
	source                TEXT NOT NULL,
	kind                  TEXT NOT NULL,
	name                  TEXT NOT NULL,
	session_id            TEXT NOT NULL DEFAULT '',
	cwd                   TEXT NOT NULL DEFAULT '',
	host                  TEXT NOT NULL DEFAULT '',
	"user"                TEXT NOT NULL DEFAULT '',
	raw                   TEXT NOT NULL DEFAULT '',
	tool_use_id           TEXT NOT NULL DEFAULT '',
	duration_ms           INTEGER NOT NULL DEFAULT 0,
	model                 TEXT NOT NULL DEFAULT '',
	input_tokens          INTEGER NOT NULL DEFAULT 0,
	output_tokens         INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
	cache_creation_tokens INTEGER NOT NULL DEFAULT 0
)`); err != nil {
		return err
	}

	have, err := existingEventColumns(ctx, tx)
	if err != nil {
		return err
	}
	for _, a := range []struct{ col, ddl string }{
		{"host", `ALTER TABLE events ADD COLUMN host TEXT NOT NULL DEFAULT ''`},
		{"user", `ALTER TABLE events ADD COLUMN "user" TEXT NOT NULL DEFAULT ''`},
		{"tool_use_id", `ALTER TABLE events ADD COLUMN tool_use_id TEXT NOT NULL DEFAULT ''`},
		{"duration_ms", `ALTER TABLE events ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0`},
		{"input_tokens", `ALTER TABLE events ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0`},
		{"output_tokens", `ALTER TABLE events ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0`},
		{"cache_read_tokens", `ALTER TABLE events ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0`},
		{"cache_creation_tokens", `ALTER TABLE events ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0`},
		{"model", `ALTER TABLE events ADD COLUMN model TEXT NOT NULL DEFAULT ''`},
	} {
		if have[a.col] {
			continue
		}
		if _, err := tx.ExecContext(ctx, a.ddl); err != nil {
			return err
		}
	}

	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_events_kind_name ON events(kind, name)`,
		`CREATE INDEX IF NOT EXISTS idx_events_source ON events(source)`,
		`CREATE INDEX IF NOT EXISTS idx_events_host ON events(host)`,
		`CREATE INDEX IF NOT EXISTS idx_events_user ON events("user")`,
		`CREATE INDEX IF NOT EXISTS idx_events_tool_use_id ON events(tool_use_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_model ON events(model)`,
	} {
		if _, err := tx.ExecContext(ctx, idx); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func existingEventColumns(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(events)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func (s *Store) Insert(ctx context.Context, e Event) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events(ts, source, kind, name, session_id, cwd, host, "user", raw, tool_use_id, duration_ms, model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.Timestamp.UTC().Format(time.RFC3339Nano),
		string(e.Source),
		string(e.Kind),
		e.Name,
		e.SessionID,
		e.Cwd,
		e.Host,
		e.User,
		e.Raw,
		e.ToolUseID,
		e.DurationMs,
		e.Model,
		e.InputTokens,
		e.OutputTokens,
		e.CacheReadTokens,
		e.CacheCreationTokens,
	)
	return err
}

// UpdateByToolUseID fills duration + usage for a previously-inserted tool row
// (skill or mcp) identified by tool_use_id. Returns the number of rows updated.
// Commands have no tool_use_id, so the kind filter is implicit.
func (s *Store) UpdateByToolUseID(ctx context.Context, toolUseID string, durationMs int64, u Usage) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE events
		   SET duration_ms = ?,
		       model = ?,
		       input_tokens = ?,
		       output_tokens = ?,
		       cache_read_tokens = ?,
		       cache_creation_tokens = ?
		 WHERE tool_use_id = ? AND duration_ms = 0`,
		durationMs, u.Model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens, toolUseID,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type PendingRow struct {
	ID        int64
	Kind      Kind
	Timestamp time.Time
}

// PendingRows returns every still-pending (duration_ms = 0) command, skill or
// mcp row in the given session, oldest first. Used by the Stop hook to
// finalize every event that opened during the turn — Codex turns can produce
// multiple pending skill rows from `$mention` injection, and Claude turns can
// still have unfinalized commands, skills, or MCP tool calls that PostToolUse
// never closed.
func (s *Store) PendingRows(ctx context.Context, sessionID string) ([]PendingRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, ts FROM events
		  WHERE session_id = ? AND duration_ms = 0
		    AND kind IN ('command', 'skill', 'mcp')
		  ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingRow
	for rows.Next() {
		var p PendingRow
		var ts string
		if err := rows.Scan(&p.ID, &p.Kind, &ts); err != nil {
			return nil, err
		}
		t, perr := time.Parse(time.RFC3339Nano, ts)
		if perr != nil {
			t, _ = time.Parse(time.RFC3339, ts)
		}
		p.Timestamp = t
		out = append(out, p)
	}
	return out, rows.Err()
}

// FinalizeRow fills duration + usage for the row with the given id, only if
// it's still pending. Returns the number of rows updated (0 means the row was
// already finalized — e.g. PostToolUse closed a skill before Stop fired).
func (s *Store) FinalizeRow(ctx context.Context, id, durationMs int64, u Usage) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE events
		   SET duration_ms = ?,
		       model = ?,
		       input_tokens = ?,
		       output_tokens = ?,
		       cache_read_tokens = ?,
		       cache_creation_tokens = ?
		 WHERE id = ? AND duration_ms = 0`,
		durationMs, u.Model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens, id,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// StartTime returns the original timestamp of an event identified by
// tool_use_id (skill or mcp). Used to compute tool duration in record cmd.
func (s *Store) StartTime(ctx context.Context, toolUseID string) (time.Time, bool, error) {
	var ts string
	err := s.db.QueryRowContext(ctx,
		`SELECT ts FROM events WHERE tool_use_id = ? ORDER BY id DESC LIMIT 1`,
		toolUseID,
	).Scan(&ts)
	if err != nil {
		if err == sql.ErrNoRows {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	t, perr := time.Parse(time.RFC3339Nano, ts)
	if perr != nil {
		t, perr = time.Parse(time.RFC3339, ts)
		if perr != nil {
			return time.Time{}, false, perr
		}
	}
	return t, true, nil
}

type Filter struct {
	Source Source
	Kind   Kind
	Host   string
	User   string
	Model  string
	Since  time.Time
	Limit  int
}

// applyFilter appends WHERE clauses for the non-zero Filter fields. It returns
// the augmented query string and the args slice (extended in place).
func applyFilter(q string, f Filter, args []any) (string, []any) {
	if f.Source != "" {
		q += ` AND source = ?`
		args = append(args, string(f.Source))
	}
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, string(f.Kind))
	}
	if f.Host != "" {
		q += ` AND host = ?`
		args = append(args, f.Host)
	}
	if f.User != "" {
		q += ` AND "user" = ?`
		args = append(args, f.User)
	}
	if f.Model != "" {
		q += ` AND model = ?`
		args = append(args, f.Model)
	}
	if !f.Since.IsZero() {
		q += ` AND ts >= ?`
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	return q, args
}

func (s *Store) Ranking(ctx context.Context, f Filter) ([]Ranking, error) {
	q, args := applyFilter(`SELECT name,
		COUNT(*) AS c,
		AVG(CASE WHEN duration_ms > 0 THEN duration_ms END) AS avg_dur,
		AVG(CASE WHEN input_tokens > 0 THEN input_tokens END) AS avg_in,
		AVG(CASE WHEN output_tokens > 0 THEN output_tokens END) AS avg_out,
		AVG(CASE WHEN (input_tokens + cache_read_tokens + cache_creation_tokens) > 0
		         THEN (input_tokens + cache_read_tokens + cache_creation_tokens) END) AS avg_ctx
		FROM events WHERE 1=1`, f, nil)
	q += ` GROUP BY name ORDER BY c DESC, name ASC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ranking
	for rows.Next() {
		var r Ranking
		var avgDur, avgIn, avgOut, avgCtx sql.NullFloat64
		if err := rows.Scan(&r.Name, &r.Count, &avgDur, &avgIn, &avgOut, &avgCtx); err != nil {
			return nil, err
		}
		r.AvgDurationMs = avgDur.Float64
		r.AvgInputTokens = avgIn.Float64
		r.AvgOutputTokens = avgOut.Float64
		r.AvgContextTokens = avgCtx.Float64
		out = append(out, r)
	}
	return out, rows.Err()
}

// NameUsage returns per-name counts and last-used timestamps for events
// matching the filter (typically a single Kind plus a Since window). It is the
// join source for the audit command: every distinct recorded name is reported
// so the caller can subtract the installed inventory to find unused entries and
// flag recorded names that no longer have a matching install (orphans). Limit
// is intentionally ignored — audit needs the full set, not a top-N slice.
func (s *Store) NameUsage(ctx context.Context, f Filter) ([]NameUsage, error) {
	q, args := applyFilter(`SELECT name, COUNT(*) AS c, MAX(ts) AS last FROM events WHERE 1=1`, f, nil)
	q += ` GROUP BY name ORDER BY c DESC, name ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NameUsage
	for rows.Next() {
		var u NameUsage
		var last string
		if err := rows.Scan(&u.Name, &u.Count, &last); err != nil {
			return nil, err
		}
		t, perr := time.Parse(time.RFC3339Nano, last)
		if perr != nil {
			t, _ = time.Parse(time.RFC3339, last)
		}
		u.LastUsed = t
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) Daily(ctx context.Context, f Filter) ([]DailyPoint, error) {
	q, args := applyFilter(`SELECT substr(ts, 1, 10) AS day, COUNT(*) FROM events WHERE 1=1`, f, nil)
	q += ` GROUP BY day ORDER BY day ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyPoint
	for rows.Next() {
		var p DailyPoint
		if err := rows.Scan(&p.Day, &p.Count); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Recent(ctx context.Context, f Filter) ([]Event, error) {
	q, args := applyFilter(`SELECT id, ts, source, kind, name, session_id, cwd, host, "user", raw, tool_use_id, duration_ms, model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens FROM events WHERE 1=1`, f, nil)
	q += ` ORDER BY id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Source, &e.Kind, &e.Name, &e.SessionID, &e.Cwd, &e.Host, &e.User, &e.Raw,
			&e.ToolUseID, &e.DurationMs, &e.Model, &e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheCreationTokens); err != nil {
			return nil, err
		}
		t, perr := time.Parse(time.RFC3339Nano, ts)
		if perr != nil {
			t, _ = time.Parse(time.RFC3339, ts)
		}
		e.Timestamp = t
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ProjectRanking(ctx context.Context, f Filter) ([]ProjectStat, error) {
	q, args := applyFilter(`SELECT cwd, COUNT(*) AS c FROM events WHERE 1=1`, f, nil)
	q += ` GROUP BY cwd ORDER BY c DESC, cwd ASC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectStat
	for rows.Next() {
		var p ProjectStat
		if err := rows.Scan(&p.Cwd, &p.Count); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) HostRanking(ctx context.Context, f Filter) ([]HostStat, error) {
	q, args := applyFilter(`SELECT host, COUNT(*) AS c FROM events WHERE 1=1`, f, nil)
	q += ` GROUP BY host ORDER BY c DESC, host ASC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostStat
	for rows.Next() {
		var h HostStat
		if err := rows.Scan(&h.Host, &h.Count); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// DistinctHosts returns all distinct host values present in the database, with
// counts. Used by the TUI to populate the host filter chip; counts are not
// limited by the filter so the picker stays stable across other filter toggles.
func (s *Store) DistinctHosts(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT host FROM events GROUP BY host ORDER BY COUNT(*) DESC, host ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) UserRanking(ctx context.Context, f Filter) ([]UserStat, error) {
	q, args := applyFilter(`SELECT "user", COUNT(*) AS c FROM events WHERE 1=1`, f, nil)
	q += ` GROUP BY "user" ORDER BY c DESC, "user" ASC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserStat
	for rows.Next() {
		var u UserStat
		if err := rows.Scan(&u.User, &u.Count); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) ModelRanking(ctx context.Context, f Filter) ([]ModelStat, error) {
	q, args := applyFilter(`SELECT model, COUNT(*) AS c FROM events WHERE 1=1`, f, nil)
	q += ` GROUP BY model ORDER BY c DESC, model ASC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelStat
	for rows.Next() {
		var m ModelStat
		if err := rows.Scan(&m.Model, &m.Count); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DistinctUsers returns all distinct user values present in the database. Same
// rationale as DistinctHosts: used by the TUI to keep the user filter picker
// stable across other filter toggles.
func (s *Store) DistinctUsers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT "user" FROM events GROUP BY "user" ORDER BY COUNT(*) DESC, "user" ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DistinctModels returns all distinct model values present in the database.
// Same rationale as DistinctHosts: used by the TUI to keep the model filter
// picker stable across other filter toggles.
func (s *Store) DistinctModels(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model FROM events GROUP BY model ORDER BY COUNT(*) DESC, model ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Total(ctx context.Context, f Filter) (int64, error) {
	q, args := applyFilter(`SELECT COUNT(*) FROM events WHERE 1=1`, f, nil)
	var c int64
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&c); err != nil {
		return 0, err
	}
	return c, nil
}
