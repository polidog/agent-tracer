# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **Project name**: `agent-tracer` (renamed from `skill-logger` at v0.2 when MCP recording was added). Code, module path (`github.com/polidog/agent-tracer`), binary, and env vars all use the new name. The legacy `skill-logger record` hook command and `~/.skill-logger/` / `SKILL_LOGGER_*` env names are intentionally kept working as fallbacks — don't rip those out until the user explicitly says they're no longer needed.

## Build / test / lint

CGO is **required** (`github.com/tursodatabase/go-libsql` links against libsql):

```sh
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test -race ./...
CGO_ENABLED=1 go test -race ./internal/store -run TestInsert   # single test
gofmt -l .                                                     # CI fails on any output
go vet ./...
golangci-lint run                                              # config in .golangci.yml
```

`go.mod` pins **Go 1.26.1**. The CI workflow (`.github/workflows/ci.yml`) runs gofmt → build → vet → `go test -race` → golangci-lint, all with `CGO_ENABLED=1`.

`golangci-lint` runs `errcheck`/`govet`/`ineffassign`/`staticcheck`/`unused`/`misspell`. `errcheck` has a curated exclusion list in `.golangci.yml` (Close, Rollback, fmt.Fprint*, tabwriter.Flush, store.Store.Close) — extend that list rather than sprinkling `_ =` assignments when adding similar idiomatic patterns.

## Architecture

Thin `main.go` → `internal/cmd` cobra root. Subcommands live alongside it:

- `cmd/root.go` — root command, persistent `--db`/`--config` flags, `loadConfig`/`openStore` helpers used by every subcommand. Default `RunE` launches the Bubble Tea TUI.
- `cmd/record.go` + `cmd/classify_test.go` — the hook ingestion path; see below.
- `cmd/mentions.go` — Codex `$skill-name` mention parser (mirrors `codex-rs/core-skills/src/injection.rs` env-var skip list).
- `cmd/mcp.go` — MCP tool name parser (`mcp__<server>__<tool>` → `server/tool`) and glob-based ignore matcher (`config.MCP.Ignore`, `path.Match` semantics).
- `cmd/stats.go` — also defines `newSyncCmd` and `newVersionCmd` (despite the filename).
- `cmd/audit.go` + `internal/inventory` — the "denominator" path. `inventory.Scanner` scans the locally-installed surface (user skills `~/.claude/skills/*/SKILL.md`, plugin skills/commands under `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/`, MCP servers from `~/.claude.json` global + `projects[].mcpServers`) and normalizes each name to the exact `events.name` form so the join is a plain string match. `audit` joins that against `store.NameUsage` (per-name COUNT + MAX(ts)) and labels each row unused/low/active, plus an orphan tail for recorded names with no install. **Known fuzziness**: some plugin "commands" (`commands/*.md`) are actually invoked through the Skill tool, so they get recorded as `kind=skill` (name w/o leading `/`) rather than `kind=command` — they then surface as a skill orphan instead of matching the command inventory. MCP inventory only covers `~/.claude.json`; servers provided by plugins or claude.ai connectors (Notion, chrome, etc.) aren't scanned and show up as MCP orphans. Both are candidate refinements (cross-kind name matching; broader MCP sources).
- `cmd/init.go` — idempotent installer that merges hook entries into `~/.claude/settings.json` and `~/.codex/config.toml`. The Pre/PostToolUse matcher is the regex `Skill|mcp__.*` (`claudeToolMatcher`), so a single entry catches both Skill and every MCP tool. Detects existing entries by substring match against `agentTracerCommandMarkers` — both the current `agent-tracer record` and the legacy `skill-logger record` are recognized as "already installed" so re-running `init --write` after upgrading from the old binary name stays idempotent.

### Hook → event pipeline (`record` command)

`record` is the single entry point for every Claude/Codex hook. The shape of the work is:

1. Read JSON from stdin → `hookPayload`. Non-JSON input is logged and exits 0 (hooks must never block the host CLI).
2. `classify(payload, source, kindOverride, nameOverride, mcpIgnore)` returns a `classifyResult{inserts, finalize}` based on `hook_event_name`:
   - `PreToolUse` + `tool_name=Skill` → insert `kind=skill`, name from `tool_input.skill` (Claude only).
   - `PreToolUse` + `tool_name=mcp__<server>__<tool>` → insert `kind=mcp`, name `server/tool`. `parseMCPTool` splits on the first `__` after the `mcp__` prefix so tool names containing underscores stay intact. `matchMCPIgnore` (path.Match glob; bare `server` is shorthand for `server/*`) drops the row before insert.
   - `UserPromptSubmit` → insert `kind=command` if prompt starts with `/`; if `--source codex`, additionally insert one `kind=skill` per `$mention` (deduped, env-var names skipped).
   - `PostToolUse` + `tool_name` in {`Skill`, `mcp__*`} → `actionFinishTool` (pairs with the matching insert via `tool_use_id`).
   - `Stop` → `actionFinishTurn` (finalize every pending row in the session).
3. Inserts go through `store.Insert`. Finalizers compute `duration_ms` from the original timestamp and pull token usage **and the model name** from `transcript_path` via `internal/transcript.LatestUsage` (Claude: `assistant.message.model`; Codex: nearest preceding `turn_context.payload.model` — token_count entries carry no model). `Usage.Model` rides with the token counts through `UpdateByToolUseID`/`FinalizeRow` into the `model` column; rows that never finalize keep `model = ''`.

The Pre/Post pairing for skills and MCP tools uses `tool_use_id`; `Store.UpdateByToolUseID` and `Store.StartTime` are kind-agnostic (commands carry no `tool_use_id`, so an implicit kind filter is fine). The `Stop` path uses `(session_id, duration_ms = 0)` over `kind IN ('command','skill','mcp')` to catch anything PostToolUse missed. **`Stop` is also the only place Codex skills ever get duration/tokens** because Codex has no Skill tool — skill bodies are injected as prompt text. The transcript parser normalizes Codex `last_token_usage` (input/cached/output) into the Claude 4-column shape so `input + cache_read + cache_creation = context size` holds for both sources (see `internal/transcript/transcript.go` doc comment).

`classify` is pure and exhaustively tested in `classify_test.go` — keep it that way; add to the table tests rather than embedding policy in the cobra `RunE`.

### Storage (`internal/store`)

`Store` wraps a single `*sql.DB`. `store.Open` switches on `cfg.Mode`:

- `local` → `sql.Open("libsql", "file:"+path)`.
- `turso` → `libsql.NewEmbeddedReplicaConnector(path, url, opts...)`. `Store.sync` is wired to the connector's `Sync()` so `agent-tracer sync` and the TUI can pull from the primary on demand.

**`Migrate` runs all DDL inside a single transaction.** This is load-bearing: Turso's embedded replica pushes WAL frames atomically per transaction, and an earlier "one Exec per statement" version produced `WAL frame insert conflict` errors on first run (see commit `4bfbc89` and `08a7f72`). SQLite has no `ADD COLUMN IF NOT EXISTS`, so `Migrate` first reads `PRAGMA table_info(events)` and skips any `ALTER` whose column already exists — do not switch to error-catching, that would abort the surrounding transaction.

All read queries flow through `applyFilter` (`Source`/`Kind`/`Host`/`User`/`Model`/`Since`). When adding a new filter dimension, extend `Filter` + `applyFilter` once instead of hand-rolling WHERE clauses per query.

### Config resolution

`internal/config` resolves settings in this order (highest wins): CLI flag → env var → `config.toml` → defaults. Specifically:

- `--db` > `AGENT_TRACER_DB` (legacy `SKILL_LOGGER_DB` accepted as fallback) > `config.db_path` > `<AGENT_TRACER_DIR>/events.db` (with `~/.agent-tracer` → `~/.skill-logger` fallback when the new dir doesn't exist yet).
- `--user`/`--host` > `AGENT_TRACER_USER`/`AGENT_TRACER_HOSTNAME` (legacy `SKILL_LOGGER_*` honored) > `config.user`/`config.hostname` > `git config user.email` / `os.Hostname()`.
- `envFirst` helper in `internal/config/config.go` centralizes the "new prefix wins, old prefix as fallback" lookup. The legacy support is deliberate — existing users with a populated `~/.skill-logger/` directory or shell-exported `SKILL_LOGGER_*` env keep working without manual migration; remove it only once those installs are known to have flipped.
- `TURSO_DATABASE_URL` being set **forces `mode = turso`** even when the config doesn't specify it.

`config.ShareRaw` is a pointer-bool so "unset" (default true) is distinguishable from explicit `false`. `record` consults `ShouldShareRaw()` before persisting `raw` — when false, the raw hook JSON column is stored as an empty string (used to keep prompts out of shared Turso DBs).

### TUI (`internal/tui`)

Bubble Tea single `Model` with 9 tabs (`Skills/Commands/MCP/Projects/Hosts/Users/Models/Daily/Recent`) plus filter chips for range/source/host/user/model. The model owns eight `table.Model`s; rendering and key handling are in `model.go`. `DistinctHosts`/`DistinctUsers`/`DistinctModels` populate the filter pickers — they intentionally **don't** apply the current filter so the picker stays stable as the user toggles other chips.

## Conventions

- Comments explain **why**, not what. The existing code is a good baseline — match its density. The store's `Migrate` and `applyFilter` and the transcript package's doc comment are examples of the desired style.
- Hooks must never block the host CLI. New code in `record` must exit 0 on any non-fatal condition (bad JSON, missing transcript, no matching tool_use_id, etc.).
- Schema changes go in `Migrate` as additive `ALTER TABLE` entries gated by the `PRAGMA table_info` check; index creation uses `CREATE INDEX IF NOT EXISTS`. Old DBs must keep working without manual intervention.
- `init --write` is idempotent by design — preserve that when touching `internal/cmd/init.go`. The "already installed" markers (`agentTracerCommandMarkers`) match by substring; both `agent-tracer record` and the legacy `skill-logger record` register as installed so re-running `init` after an upgrade is a no-op.
