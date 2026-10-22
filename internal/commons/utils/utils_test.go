package utils

import "testing"

func TestExtractThinkingAndText(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantThinking string
		wantText     string
	}{
		{
			name:         "No thinking tokens",
			input:        "Hello world",
			wantThinking: "",
			wantText:     "Hello world",
		},
		{
			name:         "Standard tags with thought",
			input:        "Hello! <ctrl94>thought\nThinking process...\n<ctrl95>Actual response here",
			wantThinking: "Thinking process...",
			wantText:     "Hello! Actual response here",
		},
		{
			name:         "No end tag",
			input:        "Hello! <ctrl94>thought\nStill thinking...",
			wantThinking: "Still thinking...",
			wantText:     "Hello!",
		},
		{
			name:         "Alternate start tag",
			input:        "<ctrl94>\nThinking...\n<ctrl95>\nDone",
			wantThinking: "Thinking...",
			wantText:     "Done",
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

// TestStripCitationArtifacts covers the artifacts observed on the wire plus the
// negative cases that must survive untouched.
func TestStripCitationArtifacts(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "Live sample, code_reference and code_stdout fence suffixes",
			input: "```python?code_reference&code_event_index=1\nprint(2 + 2)\n\n```\n\n" +
				"```text?code_stdout&code_event_index=1\n4\n```",
			want: "```python\nprint(2 + 2)\n\n```\n\n```text\n4\n```",
		},
		{
			name:  "Single inline citation marker",
			input: "The answer is 4 [cite: 3].",
			want:  "The answer is 4.",
		},
		{
			name:  "Comma-separated citation list",
			input: "Both sources agree [cite: 3, 7] on this point.",
			want:  "Both sources agree on this point.",
		},
		{
			name:  "Citation marker and cite_start in one line",
			input: "Water boils at 100C [cite_start]at sea level[cite: 12].",
			want:  "Water boils at 100C at sea level.",
		},
		{
			name:  "Marker at start of a line",
			input: "[cite: 1]First sentence.\n[cite: 2, 4]Second sentence.",
			want:  "First sentence.\nSecond sentence.",
		},
		{
			name:  "Indented fence opening line keeps its indentation",
			input: "  ```js?code_reference&code_event_index=2\nconsole.log(1)\n  ```",
			want:  "  ```js\nconsole.log(1)\n  ```",
		},
		{
			name:  "Fence suffix without extra params",
			input: "```python?code_reference\nx = 1\n```",
			want:  "```python\nx = 1\n```",
		},
		{
			name:  "Bare code_stdout suffix",
			input: "```text?code_stdout\nok\n```",
			want:  "```text\nok\n```",
		},
		{
			name:  "Negative: literal brackets and non-numeric cite-like text",
			input: "Use [1], [citation needed], [cite: many], [cite:], and [cite: 3 (approx)] as literals.",
			want:  "Use [1], [citation needed], [cite: many], [cite:], and [cite: 3 (approx)] as literals.",
		},
		{
			name:  "Negative: unfenced ?code_reference text survives",
			input: "Call the endpoint with ?code_reference&code_event_index=1 appended to the URL.",
			want:  "Call the endpoint with ?code_reference&code_event_index=1 appended to the URL.",
		},
		{
			name:  "Negative: inline code mentioning the query string survives",
			input: "Pass `?code_stdout&code_event_index=1` only after the fence marker.",
			want:  "Pass `?code_stdout&code_event_index=1` only after the fence marker.",
		},
		{
			name:  "Negative: normal markdown fences without the suffix",
			input: "```python\nprint(2 + 2)\n```\n\n```go\nfmt.Println(1)\n```",
			want:  "```python\nprint(2 + 2)\n```\n\n```go\nfmt.Println(1)\n```",
		},
		{
			name:  "Negative: unclosed citation marker survives",
			input: "An unclosed [cite: 3 marker is left alone.",
			want:  "An unclosed [cite: 3 marker is left alone.",
		},
		{
			name:  "Negative: text with no artifacts is untouched",
			input: "Plain prose with (parentheses), {braces} and ```inline fences```.",
			want:  "Plain prose with (parentheses), {braces} and ```inline fences```.",
		},
		{
			name:  "Empty input",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripCitationArtifacts(tt.input); got != tt.want {
				t.Errorf("StripCitationArtifacts() = %q, want %q", got, tt.want)
			}
		})
	}
}
