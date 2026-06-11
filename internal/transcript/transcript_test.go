package transcript

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/polidog/agent-tracer/internal/store"
)

func writeLines(t *testing.T, name string, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLatestUsageClaude(t *testing.T) {
	path := writeLines(t, "claude.jsonl",
		`{"type":"user","message":{}}`,
		`{"type":"assistant","message":{"model":"claude-haiku-4-5","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}`,
		`{"type":"assistant","message":{"model":"claude-fable-5","usage":{"input_tokens":100,"output_tokens":200,"cache_read_input_tokens":300,"cache_creation_input_tokens":400}}}`,
	)
	u, ok := LatestUsage(path, "claude")
	if !ok {
		t.Fatal("expected ok=true")
	}
	want := store.Usage{Model: "claude-fable-5", InputTokens: 100, OutputTokens: 200, CacheReadTokens: 300, CacheCreationTokens: 400}
	if u != want {
		t.Fatalf("got %#v, want %#v", u, want)
	}
}

func TestLatestUsageClaudeSyntheticModel(t *testing.T) {
	path := writeLines(t, "claude_synth.jsonl",
		`{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
	)
	u, ok := LatestUsage(path, "claude")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if u.Model != "" {
		t.Fatalf("synthetic model should be blanked, got %q", u.Model)
	}
}

func TestLatestUsageCodex(t *testing.T) {
	// Two token_count events; we should pick the latest's last_token_usage
	// and split input_tokens into non-cached + cache_read.
	path := writeLines(t, "codex.jsonl",
		`{"timestamp":"t1","type":"session_meta","payload":{}}`,
		`{"timestamp":"t2","type":"turn_context","payload":{"cwd":"/repo","model":"gpt-5.4"}}`,
		`{"timestamp":"t3","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":550},"last_token_usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":550},"model_context_window":200000},"rate_limits":null}}`,
		`{"timestamp":"t4","type":"turn_context","payload":{"cwd":"/repo","model":"gpt-5.5"}}`,
		`{"timestamp":"t5","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2000,"cached_input_tokens":800,"output_tokens":300,"reasoning_output_tokens":100,"total_tokens":2300},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":300,"output_tokens":200,"reasoning_output_tokens":50,"total_tokens":1200},"model_context_window":200000},"rate_limits":null}}`,
	)
	u, ok := LatestUsage(path, "codex")
	if !ok {
		t.Fatal("expected ok=true")
	}
	// non-cached = 1000 - 300 = 700, cache_read = 300, output = 200.
	// Model comes from the nearest preceding turn_context (gpt-5.5, not gpt-5.4).
	want := store.Usage{Model: "gpt-5.5", InputTokens: 700, OutputTokens: 200, CacheReadTokens: 300, CacheCreationTokens: 0}
	if u != want {
		t.Fatalf("got %#v, want %#v", u, want)
	}
}

func TestLatestUsageCodexNoTurnContext(t *testing.T) {
	path := writeLines(t, "codex_nomodel.jsonl",
		`{"timestamp":"t1","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0,"total_tokens":110}},"rate_limits":null}}`,
	)
	u, ok := LatestUsage(path, "codex")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if u.Model != "" {
		t.Fatalf("model should be empty without turn_context, got %q", u.Model)
	}
}

func TestLatestUsageCodexNoTokenCount(t *testing.T) {
	path := writeLines(t, "codex_empty.jsonl",
		`{"timestamp":"t1","type":"session_meta","payload":{}}`,
		`{"timestamp":"t2","type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`,
	)
	_, ok := LatestUsage(path, "codex")
	if ok {
		t.Fatal("expected ok=false when no token_count present")
	}
}

func TestLatestUsageMissingFile(t *testing.T) {
	_, ok := LatestUsage("/nonexistent/path.jsonl", "claude")
	if ok {
		t.Fatal("expected ok=false")
	}
}
