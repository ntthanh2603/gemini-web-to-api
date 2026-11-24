package utils

import (
	"encoding/json"
	"testing"
)

func TestEscapeJSONStringControlCharsRepairsInvalidEscapes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // decoded value of "v"
	}{
		{name: "backslash dollar", input: `{"v":"echo \$HOME"}`, want: `echo \$HOME`},
		{name: "backslash single quote", input: `{"v":"it\'s"}`, want: `it\'s`},
		{name: "u without four hex digits", input: `{"v":"C:\users"}`, want: `C:\users`},
		{name: "backslash at the end of the text", input: `{"v":"a\`, want: ""},
		{name: "valid escapes untouched", input: `{"v":"a\"b\\c\/d\n\u00e9"}`, want: "a\"b\\c/d\né"},
		{name: "escaped backslash before dollar", input: `{"v":"\\$x"}`, want: `\$x`},
		{name: "raw newline and invalid escape together", input: "{\"v\":\"a\n\\$b\"}", want: "a\n\\$b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repaired := EscapeJSONStringControlChars(tt.input)
			var got struct {
				V string `json:"v"`
			}
			err := json.Unmarshal([]byte(repaired), &got)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("expected unterminated input to stay invalid, got %q", repaired)
				}
				return
			}
			if err != nil {
				t.Fatalf("repaired %q does not decode: %v", repaired, err)
			}
			if got.V != tt.want {
				t.Errorf("decoded %q, want %q", got.V, tt.want)
			}
		})
	}
}

// Text outside strings is never changed, even a stray backslash.
func TestEscapeJSONStringControlCharsLeavesNonStringsAlone(t *testing.T) {
	input := `{"a":1} \$ {"b":"x"}`
	if got := EscapeJSONStringControlChars(input); got != input {
		t.Errorf("got %q, want %q", got, input)
	}
}
