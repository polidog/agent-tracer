// Package inventory scans the locally-installed Claude Code surface — skills,
// slash commands, and MCP servers — so the `audit` command can join it against
// recorded usage and surface what is installed-but-never-used. It is the
// "denominator" that the events table (the "numerator") can't see on its own.
//
// Every name is normalized to the exact form the record hook writes into
// events.name, so the audit join is a plain string match:
//
//   - user skill   -> frontmatter `name` (bare, e.g. "drizzle-orm")
//   - plugin skill -> "<plugin>:<name>"  (e.g. "code-review:code-review")
//   - user command -> "/<name>" ("/foo", nested "/dir:bar")
//   - plugin cmd   -> "/<plugin>:<name>" (e.g. "/codex:review")
//   - mcp server   -> bare server name; usage is matched by the "server/" prefix
//     since events store MCP calls as "server/tool".
//
// Scanning is best-effort: a missing directory or unreadable file is skipped
// rather than failing the whole audit. The agent's real install is the source
// of truth, so partial results beat no results.
package inventory

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/polidog/agent-tracer/internal/store"
)

// Item is one installed entry. Name is normalized to match events.name; Source
// records where it came from ("user", "plugin:<plugin>", "global", "project")
// for display and so MCP servers can note whether they're global or per-project.
type Item struct {
	Kind   store.Kind
	Name   string
	Source string
	Path   string
}

// Scanner walks a Claude home directory. Home is injectable so tests can point
// it at a fixture tree; New resolves the real ~/.claude location.
type Scanner struct {
	Home string // the user's home dir (the parent of ".claude")
}

// New returns a Scanner rooted at the current user's home directory.
func New() (*Scanner, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Scanner{Home: home}, nil
}

// Scan collects every installed skill, command, and MCP server. Results are
// deduplicated by (kind, name) — plugin caches keep multiple versions side by
// side, and an MCP server can appear in several projects, but each surfaces as
// one inventory entry. Errors from individual sources are swallowed so a broken
// plugin dir doesn't blank out the whole report.
func (s *Scanner) Scan() ([]Item, error) {
	claude := filepath.Join(s.Home, ".claude")
	var items []Item
	items = append(items, s.scanUserSkills(claude)...)
	items = append(items, s.scanUserCommands(claude)...)
	items = append(items, s.scanPlugins(claude)...)
	items = append(items, s.scanMCP()...)
	return dedupe(items), nil
}

// scanUserSkills reads ~/.claude/skills/*/SKILL.md. The invocation name is the
// frontmatter `name:` (which equals the directory name in practice), kept bare
// with no plugin prefix.
func (s *Scanner) scanUserSkills(claude string) []Item {
	dir := filepath.Join(claude, "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Item
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out = append(out, Item{
			Kind:   store.KindSkill,
			Name:   skillName(data, e.Name()),
			Source: "user",
			Path:   path,
		})
	}
	return out
}

// scanUserCommands walks ~/.claude/commands/**/*.md. Nested directories become
// a colon namespace ("commands/foo/bar.md" -> "/foo:bar"), matching how Claude
// Code exposes subdirectory commands.
func (s *Scanner) scanUserCommands(claude string) []Item {
	root := filepath.Join(claude, "commands")
	var out []Item
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		out = append(out, Item{
			Kind:   store.KindCommand,
			Name:   commandName("", rel),
			Source: "user",
			Path:   path,
		})
		return nil
	})
	return out
}

// scanPlugins walks ~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/
// and pulls skills and commands out of each. The plugin segment becomes the
// colon namespace used at invocation time. Multiple cached versions collapse in
// dedupe().
func (s *Scanner) scanPlugins(claude string) []Item {
	cache := filepath.Join(claude, "plugins", "cache")
	marketplaces, err := os.ReadDir(cache)
	if err != nil {
		return nil
	}
	var out []Item
	for _, mp := range marketplaces {
		if !mp.IsDir() {
			continue
		}
		plugins, err := os.ReadDir(filepath.Join(cache, mp.Name()))
		if err != nil {
			continue
		}
		for _, pl := range plugins {
			if !pl.IsDir() {
				continue
			}
			versions, err := os.ReadDir(filepath.Join(cache, mp.Name(), pl.Name()))
			if err != nil {
				continue
			}
			for _, ver := range versions {
				if !ver.IsDir() {
					continue
				}
				base := filepath.Join(cache, mp.Name(), pl.Name(), ver.Name())
				out = append(out, pluginSkills(base, pl.Name())...)
				out = append(out, pluginCommands(base, pl.Name())...)
			}
		}
	}
	return out
}

// pluginSkills reads <base>/skills/*/SKILL.md, naming each "<plugin>:<name>".
func pluginSkills(base, plugin string) []Item {
	dir := filepath.Join(base, "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Item
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out = append(out, Item{
			Kind:   store.KindSkill,
			Name:   plugin + ":" + skillName(data, e.Name()),
			Source: "plugin:" + plugin,
			Path:   path,
		})
	}
	return out
}

// pluginCommands reads <base>/commands/**/*.md, naming each "/<plugin>:<name>".
func pluginCommands(base, plugin string) []Item {
	root := filepath.Join(base, "commands")
	var out []Item
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		out = append(out, Item{
			Kind:   store.KindCommand,
			Name:   commandName(plugin, rel),
			Source: "plugin:" + plugin,
			Path:   path,
		})
		return nil
	})
	return out
}

// claudeConfig is the subset of ~/.claude.json we care about: the global MCP
// server map plus the per-project maps.
type claudeConfig struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	} `json:"projects"`
}

// scanMCP reads ~/.claude.json for configured MCP servers, both the global map
// and the project-scoped maps. A server present globally is marked "global";
// one seen only under projects is "project". Individual tools aren't known
// until the server runs, so inventory is at server granularity — the audit join
// matches usage by the "server/" prefix.
func (s *Scanner) scanMCP() []Item {
	data, err := os.ReadFile(filepath.Join(s.Home, ".claude.json"))
	if err != nil {
		return nil
	}
	var cfg claudeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	path := filepath.Join(s.Home, ".claude.json")
	var out []Item
	for name := range cfg.MCPServers {
		out = append(out, Item{Kind: store.KindMCP, Name: name, Source: "global", Path: path})
	}
	for _, proj := range cfg.Projects {
		for name := range proj.MCPServers {
			out = append(out, Item{Kind: store.KindMCP, Name: name, Source: "project", Path: path})
		}
	}
	return out
}

// skillName extracts the frontmatter `name:` value, falling back to the
// directory name when the file has no parseable frontmatter. It scans only the
// leading `---` fenced block so a stray "name:" deeper in the body can't win.
func skillName(data []byte, fallback string) string {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return fallback
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "name" {
			continue
		}
		name := strings.TrimSpace(val)
		name = strings.Trim(name, `"'`)
		if name != "" {
			return name
		}
	}
	return fallback
}

// commandName turns a command file's path (relative to its commands dir) into
// the invocation string the record hook stores. Nested directories join with
// ":"; a plugin prefix, when present, becomes the leading namespace. Results
// always start with "/" to match how UserPromptSubmit records the typed token.
func commandName(plugin, rel string) string {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), ".md")
	parts := strings.Split(rel, "/")
	if plugin != "" {
		parts = append([]string{plugin}, parts...)
	}
	return "/" + strings.Join(parts, ":")
}

// dedupe collapses items sharing a (kind, name). The first occurrence wins, so
// callers that want a particular Source to survive should order accordingly;
// here global MCP entries are visited before project ones via scan order.
func dedupe(items []Item) []Item {
	seen := make(map[string]bool, len(items))
	var out []Item
	for _, it := range items {
		key := string(it.Kind) + "\x00" + it.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}
