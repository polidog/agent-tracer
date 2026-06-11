package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/polidog/agent-tracer/internal/projectname"
	"github.com/polidog/agent-tracer/internal/store"
)

func newStatsCmd() *cobra.Command {
	var (
		kind   string
		source string
		host   string
		user   string
		model  string
		since  string
		limit  int
		daily  bool
		by     string
	)
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Print usage statistics to stdout",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			s, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			sinceTime, err := parseSince(since)
			if err != nil {
				return err
			}
			f := store.Filter{
				Source: store.Source(source),
				Kind:   store.Kind(kind),
				Host:   host,
				User:   user,
				Model:  model,
				Since:  sinceTime,
				Limit:  limit,
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			defer tw.Flush()

			if daily {
				points, err := s.Daily(ctx, f)
				if err != nil {
					return err
				}
				fmt.Fprintln(tw, "DAY\tCOUNT")
				for _, p := range points {
					fmt.Fprintf(tw, "%s\t%d\n", p.Day, p.Count)
				}
				return nil
			}

			switch by {
			case "", "name":
				ranks, err := s.Ranking(ctx, f)
				if err != nil {
					return err
				}
				fmt.Fprintln(tw, "RANK\tNAME\tCOUNT\tAVG_DUR\tAVG_CTX\tAVG_OUT")
				for i, r := range ranks {
					fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\n",
						i+1, r.Name, r.Count,
						fmtDuration(r.AvgDurationMs),
						fmtTokens(r.AvgContextTokens),
						fmtTokens(r.AvgOutputTokens),
					)
				}
			case "project", "cwd":
				projects, err := s.ProjectRanking(ctx, f)
				if err != nil {
					return err
				}
				folded := projectname.Fold(projects,
					func(p store.ProjectStat) string { return p.Cwd },
					func(p store.ProjectStat) int64 { return p.Count })
				if limit > 0 && len(folded) > limit {
					folded = folded[:limit]
				}
				fmt.Fprintln(tw, "RANK\tPROJECT\tCOUNT")
				for i, p := range folded {
					fmt.Fprintf(tw, "%d\t%s\t%d\n", i+1, p.Display, p.Count)
				}
			case "host":
				hosts, err := s.HostRanking(ctx, f)
				if err != nil {
					return err
				}
				fmt.Fprintln(tw, "RANK\tHOST\tCOUNT")
				for i, h := range hosts {
					name := h.Host
					if name == "" {
						name = "(unknown)"
					}
					fmt.Fprintf(tw, "%d\t%s\t%d\n", i+1, name, h.Count)
				}
			case "user":
				users, err := s.UserRanking(ctx, f)
				if err != nil {
					return err
				}
				fmt.Fprintln(tw, "RANK\tUSER\tCOUNT")
				for i, u := range users {
					name := u.User
					if name == "" {
						name = "(anonymous)"
					}
					fmt.Fprintf(tw, "%d\t%s\t%d\n", i+1, name, u.Count)
				}
			case "model":
				models, err := s.ModelRanking(ctx, f)
				if err != nil {
					return err
				}
				fmt.Fprintln(tw, "RANK\tMODEL\tCOUNT")
				for i, m := range models {
					name := m.Model
					if name == "" {
						name = "(unknown)"
					}
					fmt.Fprintf(tw, "%d\t%s\t%d\n", i+1, name, m.Count)
				}
			default:
				return fmt.Errorf("invalid --by %q (use name|project|host|user|model)", by)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind (skill|command)")
	cmd.Flags().StringVar(&source, "source", "", "filter by source (claude|codex)")
	cmd.Flags().StringVar(&host, "host", "", "filter by host (machine name recorded with each event)")
	cmd.Flags().StringVar(&user, "user", "", "filter by user (typically the email from git config user.email)")
	cmd.Flags().StringVar(&model, "model", "", "filter by model (e.g. claude-fable-5; recorded from the session transcript)")
	cmd.Flags().StringVar(&since, "since", "", "filter to events newer than this (e.g. 7d, 24h, 30m, or RFC3339 timestamp)")
	cmd.Flags().IntVar(&limit, "limit", 20, "max rows to show in ranking")
	cmd.Flags().BoolVar(&daily, "daily", false, "show a per-day timeline instead of a ranking")
	cmd.Flags().StringVar(&by, "by", "name", "ranking group: name|project|host|user|model")
	return cmd
}

func newSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Force a Turso embedded-replica sync (no-op for local SQLite)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			s, err := openStore(ctx)
			if err != nil {
				return err
			}
			defer s.Close()
			return s.Sync()
		},
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "agent-tracer 0.2.0")
		},
	}
}

func fmtDuration(ms float64) string {
	if ms <= 0 {
		return "—"
	}
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	sec := ms / 1000
	if sec < 60 {
		return fmt.Sprintf("%.1fs", sec)
	}
	return fmt.Sprintf("%dm%02ds", int(sec)/60, int(sec)%60)
}

func fmtTokens(n float64) string {
	if n <= 0 {
		return "—"
	}
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", n/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", n/1_000)
	}
	return fmt.Sprintf("%.0f", n)
}

func parseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if len(s) < 2 {
		return time.Time{}, fmt.Errorf("invalid --since %q", s)
	}
	unit := s[len(s)-1]
	nStr := s[:len(s)-1]
	n, err := strconv.Atoi(nStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --since %q: %w", s, err)
	}
	now := time.Now().UTC()
	switch unit {
	case 'd':
		return now.Add(-time.Duration(n) * 24 * time.Hour), nil
	case 'h':
		return now.Add(-time.Duration(n) * time.Hour), nil
	case 'm':
		return now.Add(-time.Duration(n) * time.Minute), nil
	case 'w':
		return now.Add(-time.Duration(n) * 7 * 24 * time.Hour), nil
	}
	return time.Time{}, fmt.Errorf("invalid --since unit in %q (use d/h/m/w or RFC3339)", s)
}
