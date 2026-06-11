// Package transcript reads session transcripts (JSONL) emitted by Claude Code
// or Codex and extracts token usage so callers can attribute context size to
// individual hook events.
package transcript

import (
	"bufio"
	"encoding/json"
	"os"

	"github.com/polidog/agent-tracer/internal/store"
)

// LatestUsage scans the transcript JSONL backwards and returns the most recent
// usage entry recognizable for the given source ("claude" or "codex"). Returns
// ok=false when the file can't be read or no usage entry is found.
//
// Mappings:
//   - claude: assistant.message.usage.{input,output,cache_read,cache_creation}_input_tokens
//     plus assistant.message.model as Usage.Model.
//   - codex:  event_msg + token_count.info.last_token_usage
//     (input_tokens, cached_input_tokens, output_tokens). Codex does not
//     distinguish cache-creation vs cache-read tokens, so cached_input_tokens
//     is mapped to CacheReadTokens and CacheCreationTokens stays 0. The
//     non-cached portion (input_tokens - cached_input_tokens) is stored as
//     InputTokens so that input + cache_read + cache_creation still equals the
//     full context size — keeping the stats query identity with Claude rows.
//     token_count carries no model name, so Usage.Model comes from the nearest
//     preceding turn_context entry (Codex writes one per turn).
func LatestUsage(path, source string) (store.Usage, bool) {
	if path == "" {
		return store.Usage{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return store.Usage{}, false
	}
	defer f.Close()

	// Transcripts can grow long; collect lines then scan from the end so we
	// don't parse every entry on huge files.
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}

	if source == string(store.SourceCodex) {
		return latestUsageCodex(lines)
	}
	return latestUsageClaude(lines)
}

type claudeEntry struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func latestUsageClaude(lines []string) (store.Usage, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if line == "" {
			continue
		}
		var e claudeEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Type != "assistant" {
			continue
		}
		u := e.Message.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
			continue
		}
		model := e.Message.Model
		if model == "<synthetic>" {
			// Claude Code injects synthetic assistant entries (e.g. errors);
			// they carry no real model name.
			model = ""
		}
		return store.Usage{
			Model:               model,
			InputTokens:         u.InputTokens,
			OutputTokens:        u.OutputTokens,
			CacheReadTokens:     u.CacheReadInputTokens,
			CacheCreationTokens: u.CacheCreationInputTokens,
		}, true
	}
	return store.Usage{}, false
}

// Codex rollout JSONL lines look like:
//
//	{"timestamp":"...","type":"event_msg","payload":{"type":"token_count",
//	 "info":{"total_token_usage":{...},"last_token_usage":{...},...}, ...}}
//
// payload.type can be many event variants; we only care about "token_count".
type codexRolloutLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type codexTurnContextPayload struct {
	Model string `json:"model"`
}

type codexTokenCountPayload struct {
	Type string `json:"type"`
	Info *struct {
		LastTokenUsage struct {
			InputTokens           int64 `json:"input_tokens"`
			CachedInputTokens     int64 `json:"cached_input_tokens"`
			OutputTokens          int64 `json:"output_tokens"`
			ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
			TotalTokens           int64 `json:"total_tokens"`
		} `json:"last_token_usage"`
	} `json:"info"`
}

func latestUsageCodex(lines []string) (store.Usage, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if line == "" {
			continue
		}
		var rl codexRolloutLine
		if err := json.Unmarshal([]byte(line), &rl); err != nil {
			continue
		}
		if rl.Type != "event_msg" || len(rl.Payload) == 0 {
			continue
		}
		var tc codexTokenCountPayload
		if err := json.Unmarshal(rl.Payload, &tc); err != nil {
			continue
		}
		if tc.Type != "token_count" || tc.Info == nil {
			continue
		}
		u := tc.Info.LastTokenUsage
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CachedInputTokens == 0 {
			continue
		}
		// Codex's input_tokens is the full input incl. cached portion. Split
		// it so input + cache_read mirrors Claude's "non-cached input + cache
		// reads" decomposition. cache_creation has no Codex analog.
		nonCached := u.InputTokens - u.CachedInputTokens
		if nonCached < 0 {
			nonCached = 0
		}
		return store.Usage{
			Model:               codexModelBefore(lines, i),
			InputTokens:         nonCached,
			OutputTokens:        u.OutputTokens,
			CacheReadTokens:     u.CachedInputTokens,
			CacheCreationTokens: 0,
		}, true
	}
	return store.Usage{}, false
}

// codexModelBefore returns the model from the nearest turn_context entry at or
// before line index i — the context that governed the turn whose token_count we
// just attributed. Empty string when the rollout has none (old Codex versions).
func codexModelBefore(lines []string, i int) string {
	for ; i >= 0; i-- {
		var rl codexRolloutLine
		if err := json.Unmarshal([]byte(lines[i]), &rl); err != nil {
			continue
		}
		if rl.Type != "turn_context" || len(rl.Payload) == 0 {
			continue
		}
		var tc codexTurnContextPayload
		if err := json.Unmarshal(rl.Payload, &tc); err != nil {
			continue
		}
		if tc.Model != "" {
			return tc.Model
		}
	}
	return ""
}
