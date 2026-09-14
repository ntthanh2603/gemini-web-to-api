package utils

import "testing"

func TestExtractThinkingAndText(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantThinking  string
		wantText      string
	}{
		{
			name:          "No thinking tokens",
			input:         "Hello world",
			wantThinking:  "",
			wantText:      "Hello world",
		},
		{
			name:          "Standard tags with thought",
			input:         "Hello! <ctrl94>thought\nThinking process...\n<ctrl95>Actual response here",
			wantThinking:  "Thinking process...",
			wantText:      "Hello! Actual response here",
		},
		{
			name:          "No end tag",
			input:         "Hello! <ctrl94>thought\nStill thinking...",
			wantThinking:  "Still thinking...",
			wantText:      "Hello!",
		},
		{
			name:          "Alternate start tag",
			input:         "<ctrl94>\nThinking...\n<ctrl95>\nDone",
			wantThinking:  "Thinking...",
			wantText:      "Done",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotThinking, gotText := ExtractThinkingAndText(tt.input)
			if gotThinking != tt.wantThinking {
				t.Errorf("ExtractThinkingAndText() gotThinking = %q, want %q", gotThinking, tt.wantThinking)
			}
			if gotText != tt.wantText {
				t.Errorf("ExtractThinkingAndText() gotText = %q, want %q", gotText, tt.wantText)
			}
		})
	}
}

func TestSplitResponseIntoChunks(t *testing.T) {
	tests := []string{
		"Hello world, this is a test.",
		"Code indentation:\n    def foo():\n        return True\n",
		"Non-spaced string: 1234567890abcdefghijklmnopqrstuvwxyz",
		"Tiếng Việt có dấu và ký tự đặc biệt: xin chào các bạn!",
		"",
	}

	for _, text := range tests {
		chunks := SplitResponseIntoChunks(text, 30)
		if text == "" {
			if len(chunks) != 0 {
				t.Errorf("expected empty chunks for empty string, got %v", chunks)
			}
			continue
		}
		joined := ""
		for _, c := range chunks {
			joined += c
		}
		if joined != text {
			t.Errorf("SplitResponseIntoChunks() reconstructed = %q, want %q", joined, text)
		}
	}
}

func TestExtractFirstJSONObject(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: `Here is the JSON: {"name": "test", "val": 123} and some trailing text`,
			want:  `{"name": "test", "val": 123}`,
		},
		{
			input: "```json\n{\"nested\": {\"key\": \"value with \\\"escaped\\\" quotes\"}}\n```",
			want:  `{"nested": {"key": "value with \"escaped\" quotes"}}`,
		},
		{
			input: `No json object here`,
			want:  "",
		},
	}

	for _, tt := range tests {
		got := ExtractFirstJSONObject(tt.input)
		if got != tt.want {
			t.Errorf("ExtractFirstJSONObject() = %q, want %q", got, tt.want)
		}
	}
}
