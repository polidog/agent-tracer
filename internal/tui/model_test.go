package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/polidog/agent-tracer/internal/store"
)

func TestFmtDuration(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "—"},
		{-1, "—"},
		{42, "42ms"},
		{999, "999ms"},
		{1000, "1.0s"},
		{1500, "1.5s"},
		{59999, "60.0s"},
		{60000, "1m00s"},
		{125000, "2m05s"},
	}
	for _, c := range cases {
		if got := fmtDuration(c.in); got != c.want {
			t.Errorf("fmtDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFmtTokens(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "—"},
		{-1, "—"},
		{1, "1"},
		{999, "999"},
		{1000, "1.0k"},
		{12500, "12.5k"},
		{999999, "1000.0k"},
		{1_000_000, "1.0M"},
		{2_500_000, "2.5M"},
	}
	for _, c := range cases {
		if got := fmtTokens(c.in); got != c.want {
			t.Errorf("fmtTokens(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRankRows(t *testing.T) {
	rs := []store.Ranking{
		{Name: "alpha", Count: 5, AvgDurationMs: 1500, AvgContextTokens: 1200},
		{Name: "beta", Count: 2},
	}
	rows := rankRows(rs)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0][0] != "1" || rows[0][1] != "alpha" || rows[0][2] != "5" {
		t.Errorf("row 0 head = %v", rows[0])
	}
	if rows[0][3] != "1.5s" || rows[0][4] != "1.2k" {
		t.Errorf("row 0 fmt = dur=%q ctx=%q", rows[0][3], rows[0][4])
	}
	// pending row → both avg cells should be "—"
	if rows[1][3] != "—" || rows[1][4] != "—" {
		t.Errorf("row 1 pending fmt = %v", rows[1])
	}
}

func TestHostRowsHandlesEmpty(t *testing.T) {
	hs := []store.HostStat{{Host: "", Count: 3}, {Host: "macbook", Count: 7}}
	rows := hostRows(hs)
	if rows[0][1] != "(unknown)" {
		t.Errorf("empty host should render as (unknown), got %q", rows[0][1])
	}
	if rows[1][1] != "macbook" {
		t.Errorf("host = %q", rows[1][1])
	}
}

func TestUserRowsHandlesEmpty(t *testing.T) {
	us := []store.UserStat{{User: "", Count: 3}, {User: "alice@x", Count: 7}}
	rows := userRows(us)
	if rows[0][1] != "(anonymous)" {
		t.Errorf("empty user should render as (anonymous), got %q", rows[0][1])
	}
	if rows[1][1] != "alice@x" {
		t.Errorf("user = %q", rows[1][1])
	}
}

func TestProjectRowsFoldsAndOrders(t *testing.T) {
	// projectRows calls projectname.Fold under the hood; we just confirm it
	// produces a slice of the right shape with counts preserved.
	ps := []store.ProjectStat{
		{Cwd: "/tmp/a", Count: 2},
		{Cwd: "/tmp/b", Count: 5},
	}
	rows := projectRows(ps)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	// The bucket with count=5 should be ranked first.
	if rows[0][2] != "5" {
		t.Errorf("top count = %q, want 5", rows[0][2])
	}
}

func TestRecentRowsRenders(t *testing.T) {
	ts := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)
	es := []store.Event{
		{
			Timestamp:   ts,
			Source:      store.SourceCodex,
			Kind:        store.KindSkill,
			Name:        "verify",
			Model:       "gpt-5.5",
			DurationMs:  2500,
			InputTokens: 100, CacheReadTokens: 200, CacheCreationTokens: 50,
		},
		{
			Timestamp: ts,
			Source:    store.SourceClaude,
			Kind:      store.KindCommand,
			Name:      "/plan",
			// never finalized — no model
		},
	}
	rows := recentRows(es)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	row := rows[0]
	if row[1] != "codex" || row[2] != "skill" || row[3] != "verify" {
		t.Errorf("src/kind/name = %v", row)
	}
	if row[4] != "gpt-5.5" {
		t.Errorf("model = %q, want gpt-5.5", row[4])
	}
	if row[5] != "2.5s" {
		t.Errorf("duration = %q, want 2.5s", row[5])
	}
	// ctx = 100 + 200 + 50 = 350
	if row[6] != "350" {
		t.Errorf("ctx = %q, want 350", row[6])
	}
	if rows[1][4] != "—" {
		t.Errorf("empty model should render as —, got %q", rows[1][4])
	}
}

func TestModelRowsHandlesEmpty(t *testing.T) {
	ms := []store.ModelStat{{Model: "", Count: 3}, {Model: "claude-fable-5", Count: 7}}
	rows := modelRows(ms)
	if rows[0][1] != "(unknown)" {
		t.Errorf("empty model should render as (unknown), got %q", rows[0][1])
	}
	if rows[1][1] != "claude-fable-5" {
		t.Errorf("model = %q", rows[1][1])
	}
}

func TestDetailFields(t *testing.T) {
	e := store.Event{
		ID:          42,
		Timestamp:   time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC),
		Source:      store.SourceClaude,
		Kind:        store.KindSkill,
		Name:        "verify",
		Model:       "claude-fable-5",
		SessionID:   "sess1",
		Cwd:         "/repo",
		Host:        "mac1",
		User:        "alice@x",
		ToolUseID:   "tu_1",
		DurationMs:  2500,
		InputTokens: 100, OutputTokens: 50, CacheReadTokens: 200, CacheCreationTokens: 50,
	}
	fields := detailFields(e)
	byLabel := map[string]string{}
	for _, f := range fields {
		byLabel[f[0]] = f[1]
	}
	if byLabel["Model"] != "claude-fable-5" {
		t.Errorf("Model = %q", byLabel["Model"])
	}
	if byLabel["Duration"] != "2.5s" {
		t.Errorf("Duration = %q", byLabel["Duration"])
	}
	// context = 100 + 200 + 50 = 350
	if byLabel["Context"] != "350" {
		t.Errorf("Context = %q", byLabel["Context"])
	}
	if byLabel["Session"] != "sess1" || byLabel["Tool use ID"] != "tu_1" {
		t.Errorf("session/tool_use_id = %q / %q", byLabel["Session"], byLabel["Tool use ID"])
	}

	// Pending event with no model/session → dashes.
	fields = detailFields(store.Event{})
	byLabel = map[string]string{}
	for _, f := range fields {
		byLabel[f[0]] = f[1]
	}
	if byLabel["Model"] != "—" || byLabel["Duration"] != "—" || byLabel["Session"] != "—" {
		t.Errorf("empty event fields = %v", byLabel)
	}
}

func TestDetailOverlayOpenClose(t *testing.T) {
	m := New(nil)
	m.tab = tabRecent
	m.recent = []store.Event{{ID: 1, Name: "verify", Model: "claude-fable-5"}}
	m.recentTbl.SetRows(recentRows(m.recent))

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.detail == nil || m.detail.Name != "verify" {
		t.Fatalf("enter on Recent should open detail, got %+v", m.detail)
	}

	// While open, navigation keys are swallowed (tab must not change).
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.tab != tabRecent || m.detail == nil {
		t.Fatalf("tab key should be swallowed while detail open")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.detail != nil {
		t.Fatal("esc should close detail")
	}

	// Enter on a non-Recent tab must not open the overlay.
	m.tab = tabSkills
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.detail != nil {
		t.Fatal("enter on Skills tab should not open detail")
	}
}

func TestModelLoadFiltersSeedCurrentHostAndUser(t *testing.T) {
	// currentHost / currentUser must return "" when no entry is selected,
	// and the indexed value otherwise. Smoke test the bounds-clamping that
	// happens after a refresh shrinks the user/host list.
	m := Model{
		hosts: []string{"mac1", "mac2"},
		users: []string{"alice@x", "bob@x"},
	}
	if m.currentHost() != "" || m.currentUser() != "" {
		t.Errorf("default index 0 should mean no filter")
	}
	m.hostI = 1
	m.userI = 2
	if m.currentHost() != "mac1" {
		t.Errorf("hostI=1 → %q", m.currentHost())
	}
	if m.currentUser() != "bob@x" {
		t.Errorf("userI=2 → %q", m.currentUser())
	}
	// Out-of-range index → fallback to "".
	m.hostI = 99
	if m.currentHost() != "" {
		t.Errorf("out-of-range hostI should return empty, got %q", m.currentHost())
	}
}
