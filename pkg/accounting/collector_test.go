package accounting

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

var errTest = errors.New("boom")

// splitLines splits raw JSONL bytes into individual lines, dropping any
// trailing blank line left by the file's final newline.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func feed(t *testing.T, c *Collector, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range splitLines(data) {
		c.Observe(line)
	}
}

func TestCollectorClaudeTerminalResult(t *testing.T) {
	c := NewCollector(UsageHint{})
	feed(t, c, "testdata/claude-opus-4-8.jsonl")
	obs := c.Finish(nil)
	if !obs.Metered || obs.Schema != SchemaAnthropic || obs.Model != "claude-opus-4-8" {
		t.Fatalf("obs = %+v", obs)
	}
	if obs.Tokens.CacheRead != 15940 || obs.Tokens.CacheWrite1h != 18551 || obs.Tokens.Output != 345 {
		t.Fatalf("tokens = %+v", obs.Tokens)
	}
	if obs.ReportedCostUSD == nil || *obs.ReportedCostUSD != 0.202115 {
		t.Fatalf("reported = %v", obs.ReportedCostUSD)
	}
}

func TestCollectorRepeatedAssistantNotSummed(t *testing.T) {
	c := NewCollector(UsageHint{})
	feed(t, c, "testdata/glm-5.3.jsonl") // contains repeated assistant ids + one result
	obs := c.Finish(nil)
	if obs.Tokens.UncachedInput != 48583 || obs.Tokens.CacheRead != 47616 || obs.Tokens.Output != 1130 {
		t.Fatalf("assistant usage must not be summed; got %+v", obs.Tokens)
	}
	if obs.Model != "glm-5.3" {
		t.Fatalf("model = %q", obs.Model)
	}
}

func TestCollectorInterruptedIsUnmetered(t *testing.T) {
	c := NewCollector(UsageHint{Model: "claude-opus-4-8"})
	c.Observe([]byte(`{"type":"assistant","message":{"model":"claude-opus-4-8","content":[]}}`))
	obs := c.Finish(errTest)
	if obs.Metered {
		t.Fatal("no terminal result → unmetered")
	}
	if obs.Reason != ReasonInterrupted {
		t.Fatalf("reason = %q", obs.Reason)
	}
	if obs.Model != "claude-opus-4-8" { // hint/discovery preserved for coverage
		t.Fatalf("model = %q", obs.Model)
	}
}

// --- Additional collector tests: the synthetic adapter envelope (Task 7 will
// emit this shape from codex/openai/openai-agent), not exercised by the three
// tests above.

func TestCollectorSyntheticAdapterEnvelope(t *testing.T) {
	c := NewCollector(UsageHint{})
	feed(t, c, "testdata/codex-gpt-5.6-sol.jsonl")
	obs := c.Finish(nil)
	if !obs.Metered || obs.Schema != SchemaOpenAI || obs.Model != "gpt-5.6-sol" || obs.Channel != "codex" {
		t.Fatalf("obs = %+v", obs)
	}
	if obs.Tokens.UncachedInput != 22005-10752 || obs.Tokens.CacheRead != 10752 || obs.Tokens.Output != 8 {
		t.Fatalf("tokens = %+v", obs.Tokens)
	}
	if obs.ReportedCostUSD != nil {
		t.Fatalf("adapter envelope carries no vendor cost, got %v", obs.ReportedCostUSD)
	}
}

func TestCollectorUpstreamUsagesAreSummedHere(t *testing.T) {
	c := NewCollector(UsageHint{})
	c.Observe([]byte(`{"type":"result","subtype":"success","usage_contract_version":1,"usage_schema":"openai_chat","channel":"openai-agent","model":"gpt-turn","upstream_usages":[{"prompt_tokens":100,"completion_tokens":10},{"prompt_tokens":50,"prompt_cached_tokens":20,"completion_tokens":5}]}`))
	obs := c.Finish(nil)
	if !obs.Metered || obs.Schema != SchemaOpenAIChat {
		t.Fatalf("obs = %+v", obs)
	}
	// call 1: uncached=100, output=10; call 2: uncached=30, cache_read=20, output=5
	if obs.Tokens.UncachedInput != 130 || obs.Tokens.CacheRead != 20 || obs.Tokens.Output != 15 {
		t.Fatalf("upstream usages must be summed by the collector, got %+v", obs.Tokens)
	}
}

func TestCollectorUnknownContractVersionIsUnsupported(t *testing.T) {
	c := NewCollector(UsageHint{})
	c.Observe([]byte(`{"type":"result","usage_contract_version":2,"usage_schema":"openai","channel":"codex","model":"gpt-x","usage":{"input_tokens":1,"output_tokens":1}}`))
	obs := c.Finish(nil)
	if obs.Metered {
		t.Fatal("unknown usage_contract_version must be unmetered")
	}
	if obs.Reason != ReasonUnsupported {
		t.Fatalf("reason = %q", obs.Reason)
	}
	if len(obs.Warnings) == 0 {
		t.Fatal("expected a warning explaining the unsupported version")
	}
}

func TestCollectorMissingTerminalNoExitErr(t *testing.T) {
	c := NewCollector(UsageHint{})
	obs := c.Finish(nil)
	if obs.Metered {
		t.Fatal("no lines observed at all → unmetered")
	}
	if obs.Reason != ReasonMissingTerminal {
		t.Fatalf("reason = %q", obs.Reason)
	}
}
