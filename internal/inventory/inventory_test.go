package inventory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/polidog/agent-tracer/internal/store"
)

func TestSkillName(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		fallback string
		want     string
	}{
		{"quoted", "---\nname: \"drizzle-orm\"\ndescription: x\n---\nbody", "dir", "drizzle-orm"},
		{"unquoted", "---\nname: git-wt\n---", "dir", "git-wt"},
		{"single-quoted", "---\nname: 'foo'\n---", "dir", "foo"},
		{"no frontmatter", "# heading\nname: notthis", "fallbackdir", "fallbackdir"},
		{"empty name falls back", "---\nname:\n---", "dir", "dir"},
		{"name only in body ignored", "---\ndescription: x\n---\nname: body", "dir", "dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skillName([]byte(tt.data), tt.fallback); got != tt.want {
				t.Errorf("skillName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCommandName(t *testing.T) {
	tests := []struct {
		plugin string
		rel    string
		want   string
	}{
		{"", "dev.md", "/dev"},
		{"", filepath.Join("sub", "bar.md"), "/sub:bar"},
		{"codex", "review.md", "/codex:review"},
		{"commit-commands", "commit.md", "/commit-commands:commit"},
		{"vercel", filepath.Join("deploy", "prod.md"), "/vercel:deploy:prod"},
	}
	for _, tt := range tests {
		if got := commandName(tt.plugin, tt.rel); got != tt.want {
			t.Errorf("commandName(%q, %q) = %q, want %q", tt.plugin, tt.rel, got, tt.want)
		}
	}
}

func TestDedupeCollapsesAndSorts(t *testing.T) {
	in := []Item{
		{Kind: store.KindMCP, Name: "floq", Source: "global"},
		{Kind: store.KindMCP, Name: "floq", Source: "project"}, // dup, global wins
		{Kind: store.KindSkill, Name: "b-skill"},
		{Kind: store.KindSkill, Name: "a-skill"},
	}
	out := dedupe(in)
	if len(out) != 3 {
		t.Fatalf("dedupe len = %d, want 3", len(out))
	}
	// command < mcp < skill alphabetically among kind strings: "mcp" < "skill".
	if out[0].Kind != store.KindMCP || out[0].Source != "global" {
		t.Errorf("first item = %+v, want global floq", out[0])
	}
	if out[1].Name != "a-skill" || out[2].Name != "b-skill" {
		t.Errorf("skills not sorted: %q, %q", out[1].Name, out[2].Name)
	}
}

func TestScanFixture(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")

	// user skill
	writeFile(t, filepath.Join(claude, "skills", "drizzle-orm", "SKILL.md"),
		"---\nname: drizzle-orm\ndescription: x\n---\n")
	// user command (nested)
	writeFile(t, filepath.Join(claude, "commands", "deploy", "prod.md"), "go")
	// plugin skill + command
	pbase := filepath.Join(claude, "plugins", "cache", "official", "codex", "1.0.0")
	writeFile(t, filepath.Join(pbase, "skills", "rescue", "SKILL.md"),
		"---\nname: rescue\n---\n")
	writeFile(t, filepath.Join(pbase, "commands", "review.md"), "go")
	// a second cached version of the same plugin skill -> must collapse
	pbase2 := filepath.Join(claude, "plugins", "cache", "official", "codex", "1.0.2")
	writeFile(t, filepath.Join(pbase2, "skills", "rescue", "SKILL.md"),
		"---\nname: rescue\n---\n")
	// ~/.claude.json with global + project MCP servers
	writeFile(t, filepath.Join(home, ".claude.json"),
		`{"mcpServers":{"floq":{}},"projects":{"/p":{"mcpServers":{"sentry":{}}}}}`)

	s := &Scanner{Home: home}
	items, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	got := map[string]string{} // name -> source
	for _, it := range items {
		got[string(it.Kind)+" "+it.Name] = it.Source
	}
	want := map[string]string{
		"skill drizzle-orm":     "user",
		"skill codex:rescue":    "plugin:codex",
		"command /deploy:prod":  "user",
		"command /codex:review": "plugin:codex",
		"mcp floq":              "global",
		"mcp sentry":            "project",
	}
	for k, src := range want {
		if got[k] != src {
			t.Errorf("item %q source = %q, want %q (all: %v)", k, got[k], src, got)
		}
	}
	if len(items) != len(want) {
		t.Errorf("scanned %d items, want %d: %v", len(items), len(want), got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
