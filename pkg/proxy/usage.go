package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// UsageRecord captures the usage of a single proxied response: the model, the
// token counts (input/output/cache), and the request/response byte sizes,
// anchored to the moment of the request. AppendUsageRecord writes one record
// per line to usage.jsonl.
//
// JSON field names match pkg/proxy/.usages/usage_log.md exactly (camelCase).
// Timestamp is a real time.Time — encoding/json serializes it as RFC3339.
type UsageRecord struct {
	Timestamp           time.Time `json:"timestamp"`
	Model               string    `json:"model"`
	InputTokens         int       `json:"inputTokens"`
	OutputTokens        int       `json:"outputTokens"`
	CacheCreationTokens int       `json:"cacheCreationTokens"`
	CacheReadTokens     int       `json:"cacheReadTokens"`
	RequestBytes        int       `json:"requestBytes"`
	ResponseBytes       int       `json:"responseBytes"`
}

// ParseUsage parses a proxied response body into a UsageRecord, generalizing
// the SSE-parsing logic in zai.go to every upstream and both response modes.
//
// contentType: the response's Content-Type header.
// body:        the accumulated response body bytes.
//
// ParseUsage fills only Model and the four token fields. RequestBytes and
// ResponseBytes are left zero and are filled by the caller (Proxy.ServeHTTP).
//
// A response with no resolvable usage field (for example a non-200 error body)
// yields an error and a zero-valued UsageRecord — never a partial record.
func ParseUsage(contentType, body string) (UsageRecord, error) {
	if strings.Contains(contentType, "text/event-stream") {
		return parseUsageFromSSE(body)
	}
	return parseUsageFromJSON(body)
}

// parseUsageFromSSE reuses zai.go's parseSSE to accumulate Model and the usage
// map from message_start/message_delta events into one record.
func parseUsageFromSSE(body string) (UsageRecord, error) {
	msg, apiErr := parseSSE([]byte(body))
	if apiErr != nil {
		return UsageRecord{}, fmt.Errorf("parse SSE usage: %s", apiErr.Message)
	}
	if msg == nil || msg.Usage == nil {
		return UsageRecord{}, errors.New("parse SSE usage: no usage field")
	}
	return usageRecordFromMap(msg.Model, msg.Usage), nil
}

// parseUsageFromJSON parses a single JSON message — either a plain non-streaming
// Anthropic response or ZAITransform's reassembled single-JSON output — reading
// .model and .usage directly.
func parseUsageFromJSON(body string) (UsageRecord, error) {
	var resp struct {
		Model string         `json:"model"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return UsageRecord{}, fmt.Errorf("parse JSON usage: %w", err)
	}
	if resp.Usage == nil {
		return UsageRecord{}, errors.New("parse JSON usage: no usage field")
	}
	return usageRecordFromMap(resp.Model, resp.Usage), nil
}

// usageRecordFromMap fills Model and the four token fields from an Anthropic
// usage map (input_tokens / output_tokens / cache_creation_input_tokens /
// cache_read_input_tokens). sseInt safely reads missing keys as zero.
func usageRecordFromMap(model string, usage map[string]any) UsageRecord {
	return UsageRecord{
		Model:               model,
		InputTokens:         sseInt(usage, "input_tokens"),
		OutputTokens:        sseInt(usage, "output_tokens"),
		CacheCreationTokens: sseInt(usage, "cache_creation_input_tokens"),
		CacheReadTokens:     sseInt(usage, "cache_read_input_tokens"),
	}
}

// AppendUsageRecord appends record as a single JSON line to path (for example
// .afm/runs/<run>/usage.jsonl), creating the file if it does not exist.
//
// One call is one open-write-close cycle: no handle is held across calls,
// matching pkg/state.SaveFeedback's pattern.
func AppendUsageRecord(path string, record UsageRecord) error {
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal usage record: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open usage log: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write usage record: %w", err)
	}
	return nil
}
