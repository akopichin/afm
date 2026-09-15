package mcp_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/mcp"
)

func TestDialogSnippet(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		// First-line extraction
		{
			name: "single line",
			text: "Hello world",
			want: "Hello world",
		},
		{
			name: "multi-line, first line only",
			text: "First line\nSecond line",
			want: "First line",
		},
		{
			name: "multi-line with many lines",
			text: "Line 1\nLine 2\nLine 3",
			want: "Line 1",
		},
		// Trimming
		{
			name: "leading whitespace trimmed",
			text: "  Hello  \nSecond",
			want: "Hello",
		},
		{
			name: "trailing whitespace trimmed",
			text: "Hello  \nSecond",
			want: "Hello",
		},
		{
			name: "leading and trailing trimmed",
			text: "  Hello  \nSecond",
			want: "Hello",
		},
		// Skipping leading blank lines
		{
			name: "leading blank line skipped",
			text: "\nFirst content",
			want: "First content",
		},
		{
			name: "multiple leading blank lines skipped",
			text: "\n\n\nFirst content\nSecond",
			want: "First content",
		},
		{
			name: "leading blank lines with whitespace skipped",
			text: "   \n  \nFirst content",
			want: "First content",
		},
		// Truncation with ellipsis
		{
			name: "exactly 120 runes",
			text: string(make([]rune, 120)),
			want: string(make([]rune, 120)),
		},
		{
			name: "121 runes truncated with ellipsis",
			text: string(make([]rune, 121)),
			want: string(make([]rune, 120)) + "…",
		},
		{
			name: "200 runes truncated to 120 + ellipsis",
			text: string(make([]rune, 200)),
			want: string(make([]rune, 120)) + "…",
		},
		// Multibyte rune safety
		{
			name: "Cyrillic text, single line",
			text: "Привет мир",
			want: "Привет мир",
		},
		{
			name: "Cyrillic text with newline",
			text: "Привет мир\nВторая строка",
			want: "Привет мир",
		},
		{
			name: "emoji in text within limit",
			text: "Hello 😀 world with emoji",
			want: "Hello 😀 world with emoji",
		},
		{
			name: "multibyte runes at truncation boundary",
			// Create a string that's exactly 120 runes after truncation point
			text: "Start " + string(make([]rune, 150)) + " end",
			want: "Start " + string(make([]rune, 114)) + "…",
		},
		// Empty and whitespace-only
		{
			name: "empty string",
			text: "",
			want: "",
		},
		{
			name: "whitespace only",
			text: "   ",
			want: "",
		},
		{
			name: "whitespace and newline only",
			text: "  \n  \n  ",
			want: "",
		},
		{
			name: "tab and space only",
			text: "\t \t ",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mcp.DialogSnippet(tt.text)
			if got != tt.want {
				t.Errorf("DialogSnippet(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestDialogFeedNotice(t *testing.T) {
	tests := []struct {
		name  string
		phase string
		id    string
		title string
	}{
		{
			name:  "basic",
			phase: "planning",
			id:    "q1",
			title: "What should we do?",
		},
		{
			name:  "implementation phase",
			phase: "implementation",
			id:    "q2",
			title: "Choose an option",
		},
		{
			name:  "review phase",
			phase: "review",
			id:    "q3",
			title: "Approve?",
		},
		{
			name:  "empty values",
			phase: "",
			id:    "",
			title: "",
		},
		{
			name:  "special characters",
			phase: "test-phase",
			id:    "test_id_123",
			title: "Title with special chars: !@#$%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mcp.DialogFeedNotice(tt.phase, tt.id, tt.title)

			// Check that it's a map[string]any
			if got == nil {
				t.Error("DialogFeedNotice returned nil")
				return
			}

			// Check exact keys
			if len(got) != 3 {
				t.Errorf("DialogFeedNotice returned %d keys, want 3", len(got))
			}

			// Check each value
			if gotPhase, ok := got["phase"]; !ok || gotPhase != tt.phase {
				t.Errorf("phase: got %v, want %v", gotPhase, tt.phase)
			}

			if gotID, ok := got["id"]; !ok || gotID != tt.id {
				t.Errorf("id: got %v, want %v", gotID, tt.id)
			}

			if gotTitle, ok := got["title"]; !ok || gotTitle != tt.title {
				t.Errorf("title: got %v, want %v", gotTitle, tt.title)
			}
		})
	}
}
