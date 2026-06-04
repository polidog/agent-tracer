package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/polidog/agent-tracer/internal/inventory"
	"github.com/polidog/agent-tracer/internal/store"
)

// lowUsageThreshold is the inclusive upper bound for the "low" status: an entry
// used at least once but fewer than this many times in the window is flagged as
// underused (a candidate to revisit), while 0 is "unused" and >= is "active".
const lowUsageThreshold = 3

// auditRow is one reconciled line: an installed item joined with its usage (or
// usage with no install, when Installed is false — an orphan).
type auditRow struct {
	Name      string
	Source    string
	Installed bool
	Count     int64
	LastUsed  time.Time
}

func (r auditRow) status() string {
	switch {
	case !r.Installed:
		return "orphan"
	case r.Count == 0:
		return "unused"
	case r.Count < lowUsageThreshold:
		return "low"
	default:
		return "active"
	}
}

func newAuditCmd() *cobra.Command {
	var (
		kind   string
		source string
		host   string
		user   string
		since  string
		unused bool
	)
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Reconcile installed skills/commands/MCP servers against recorded usage",
		Long: `Scan the locally-installed Claude Code surface (skills under ~/.claude/skills
and plugin caches, slash commands, and MCP servers from ~/.claude.json) and join
it against recorded usage to show what is installed but never (or rarely) used.

STATUS column:
  unused  installed but 0 calls in the window  -> removal candidate
  low     1..2 calls in the window             -> underused, worth a look
  active  3+ calls in the window
  orphan  recorded usage with no matching install (uninstalled or built-in)

MCP servers are reconciled at server granularity: a server counts as used when
any "server/tool" call was recorded for it. Use --since to set the window
(default 30d) and --unused to list only the removal candidates.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			if since == "" {
				since = "30d"
			}
			sinceTime, err := parseSince(since)
			if err != nil {
				return err
			}

			scanner, err := inventory.New()
			if err != nil {
				return err
			}
			items, err := scanner.Scan()
			if err != nil {
				return err
			}

			s, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			now := time.Now()
			out := cmd.OutOrStdout()

			kinds := []store.Kind{store.KindSkill, store.KindCommand, store.KindMCP}
			if kind != "" {
				kinds = []store.Kind{store.Kind(kind)}
			}

			for _, k := range kinds {
				usage, err := s.NameUsage(ctx, store.Filter{
					Source: store.Source(source),
					Kind:   k,
					Host:   host,
					User:   user,
					Since:  sinceTime,
				})
				if err != nil {
					return err
				}
				rows := reconcile(k, filterKind(items, k), usage)
				printKind(out, k, rows, now, unused)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "limit to one kind (skill|command|mcp)")
	cmd.Flags().StringVar(&source, "source", "", "filter usage by source (claude|codex)")
	cmd.Flags().StringVar(&host, "host", "", "filter usage by host")
	cmd.Flags().StringVar(&user, "user", "", "filter usage by user")
	cmd.Flags().StringVar(&since, "since", "30d", "usage window (e.g. 7d, 24h, 30m, or RFC3339)")
	cmd.Flags().BoolVar(&unused, "unused", false, "show only removal candidates (unused entries)")
	return cmd
}

func filterKind(items []inventory.Item, k store.Kind) []inventory.Item {
	var out []inventory.Item
	for _, it := range items {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

// reconcile joins installed items of one kind with the recorded usage for that
// kind. Skills and commands match on the exact name. MCP servers match on the
// "server/" prefix of recorded "server/tool" names — usage is aggregated up to
// the server. Recorded names with no matching install are emitted as orphans.
func reconcile(k store.Kind, items []inventory.Item, usage []store.NameUsage) []auditRow {
	if k == store.KindMCP {
		return reconcileMCP(items, usage)
	}

	byName := make(map[string]store.NameUsage, len(usage))
	for _, u := range usage {
		byName[u.Name] = u
	}
	matched := make(map[string]bool, len(items))

	var rows []auditRow
	for _, it := range items {
		u := byName[it.Name] // zero value when never used
		matched[it.Name] = true
		rows = append(rows, auditRow{
			Name:      it.Name,
			Source:    it.Source,
			Installed: true,
			Count:     u.Count,
			LastUsed:  u.LastUsed,
		})
	}
	for _, u := range usage {
		if matched[u.Name] {
			continue
		}
		rows = append(rows, auditRow{Name: u.Name, Source: "—", Count: u.Count, LastUsed: u.LastUsed})
	}
	return rows
}

func reconcileMCP(items []inventory.Item, usage []store.NameUsage) []auditRow {
	// Aggregate recorded "server/tool" usage up to the server.
	type agg struct {
		count int64
		last  time.Time
	}
	byServer := map[string]*agg{}
	for _, u := range usage {
		server, _, found := strings.Cut(u.Name, "/")
		if !found {
			server = u.Name
		}
		a := byServer[server]
		if a == nil {
			a = &agg{}
			byServer[server] = a
		}
		a.count += u.Count
		if u.LastUsed.After(a.last) {
			a.last = u.LastUsed
		}
	}
	matched := make(map[string]bool, len(items))

	var rows []auditRow
	for _, it := range items {
		matched[it.Name] = true
		a := byServer[it.Name]
		row := auditRow{Name: it.Name, Source: it.Source, Installed: true}
		if a != nil {
			row.Count = a.count
			row.LastUsed = a.last
		}
		rows = append(rows, row)
	}
	// Orphan servers: recorded calls whose server isn't configured anymore.
	servers := make([]string, 0, len(byServer))
	for server := range byServer {
		servers = append(servers, server)
	}
	sort.Strings(servers)
	for _, server := range servers {
		if matched[server] {
			continue
		}
		a := byServer[server]
		rows = append(rows, auditRow{Name: server, Source: "—", Count: a.count, LastUsed: a.last})
	}
	return rows
}

func printKind(out io.Writer, k store.Kind, rows []auditRow, now time.Time, unusedOnly bool) {
	// Stable, useful ordering: unused first (the actionable ones), then by count
	// descending, then name. Orphans sort last regardless.
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := rows[i], rows[j]
		if ri.Installed != rj.Installed {
			return ri.Installed // installed before orphans
		}
		if (ri.Count == 0) != (rj.Count == 0) {
			return ri.Count == 0 // unused before used
		}
		if ri.Count != rj.Count {
			return ri.Count > rj.Count
		}
		return ri.Name < rj.Name
	})

	var installed, used int
	for _, r := range rows {
		if r.Installed {
			installed++
			if r.Count > 0 {
				used++
			}
		}
	}

	fmt.Fprintf(out, "\n%s — %d installed, %d used, %d unused\n",
		strings.ToUpper(string(k)), installed, used, installed-used)

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSRC\tCOUNT\tLAST_USED\tSTATUS")
	shown := 0
	for _, r := range rows {
		if unusedOnly && r.status() != "unused" {
			continue
		}
		count := "—"
		if r.Count > 0 {
			count = fmt.Sprintf("%d", r.Count)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Source, count, fmtAgo(r.LastUsed, now), r.status())
		shown++
	}
	if shown == 0 {
		fmt.Fprintln(tw, "(none)")
	}
	tw.Flush()
}

// fmtAgo renders a coarse "time since" suitable for an at-a-glance table. Zero
// time (never used) renders as an em dash.
func fmtAgo(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
