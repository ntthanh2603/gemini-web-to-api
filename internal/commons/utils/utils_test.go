package utils

import "testing"

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "empty", text: "", want: 0},
		// 11 non-CJK runes -> ceil(11/4) = 3
		{name: "english sentence", text: "Hello world", want: 3},
		// rune-based: 5 Han runes count ~1 token each; bytes/4 would say 3
		{name: "cjk one token per rune", text: "你好世界啊", want: 5},
		{name: "japanese kana", text: "こんにちは", want: 5},
		{name: "korean hangul", text: "안녕하세요", want: 5},
		// Vietnamese: 44 runes, 0 CJK -> 11; bytes/4 would overcount (~1.5x)
		{name: "vietnamese multibyte", text: "Xin chào, đây là một câu tiếng Việt để thử", want: 11},
		// 2 CJK + 4 other -> 2 + ceil(4/4) = 3
		{name: "mixed scripts", text: "你好 abc", want: 3},
		{name: "longer english", text: "The quick brown fox jumps over the lazy dog again and again and again and again ok", want: 21},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EstimateTokens(tt.text); got != tt.want {
				t.Errorf("EstimateTokens(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

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

func TestEscapeJSONStringControlChars(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "{\"body\":\"# Hello\n\nSome\tmarkdown\r\"}",
			want:  `{"body":"# Hello\n\nSome\tmarkdown\r"}`,
		},
		{
			// Whitespace between tokens and existing escapes stay as they are.
			input: "{\n  \"a\": \"x\\\"\n\\\\\",\n  \"b\": \"\x01\"\n}",
			want:  "{\n  \"a\": \"x\\\"\\n\\\\\",\n  \"b\": \"\\u0001\"\n}",
		},
		{
			input: `{"already":"valid\n"}`,
			want:  `{"already":"valid\n"}`,
		},
	}

	for _, tt := range tests {
		got := EscapeJSONStringControlChars(tt.input)
		if got != tt.want {
			t.Errorf("EscapeJSONStringControlChars(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
