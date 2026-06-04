package cmd

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/polidog/agent-tracer/internal/config"
	"github.com/polidog/agent-tracer/internal/store"
	"github.com/polidog/agent-tracer/internal/tui"
)

var (
	flagDBPath     string
	flagConfigPath string
)

func New() *cobra.Command {
	root := &cobra.Command{
		Use:   "agent-tracer",
		Short: "Record and view Claude Code / Codex skill, slash command, and MCP tool usage",
		Long: `agent-tracer records which Skills, slash commands, and MCP tools you use in
Claude Code (and optionally Codex) and lets you browse the stats in a terminal UI.

Run "agent-tracer" with no arguments to launch the TUI.

Configure your ~/.claude/settings.json hooks so that PreToolUse(Skill|mcp__.*) and
UserPromptSubmit pipe their JSON payload into "agent-tracer record" — see the
README for a copy-pasteable snippet.

Storage backend (local SQLite vs Turso Embedded Replicas) is selected via
~/.agent-tracer/config.toml (legacy ~/.skill-logger/config.toml is also read
as a fallback). See the README for fields.`,
		SilenceUsage: true,
		RunE:         runTUI,
	}
	root.PersistentFlags().StringVar(&flagDBPath, "db", "", "override db_path from config (file path)")
	root.PersistentFlags().StringVar(&flagConfigPath, "config", "", "path to config.toml (default: $AGENT_TRACER_CONFIG or ~/.agent-tracer/config.toml; legacy $SKILL_LOGGER_CONFIG / ~/.skill-logger/config.toml also honored)")

	root.AddCommand(newRecordCmd())
	root.AddCommand(newStatsCmd())
	root.AddCommand(newAuditCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newInitCmd())
	return root
}

func runTUI(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	p := tea.NewProgram(tui.New(s), tea.WithAltScreen(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load(flagConfigPath)
	if err != nil {
		return nil, err
	}
	if flagDBPath != "" {
		cfg.DBPath = flagDBPath
	}
	return cfg, nil
}

func openStore(ctx context.Context) (*store.Store, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := s.Migrate(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func Execute() {
	if err := New().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
