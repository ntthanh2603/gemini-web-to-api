package dto

import (
	"encoding/json"
	"testing"
)

func TestChatCompletionRequestPreservesReasoningEffort(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high"} {
		t.Run(effort, func(t *testing.T) {
			var req ChatCompletionRequest
			input := `{"model":"gemini-3.6-flash","reasoning_effort":"` + effort + `","messages":[{"role":"user","content":"Hello"}]}`
			if err := json.Unmarshal([]byte(input), &req); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			if result["reasoning_effort"] != effort {
				t.Fatalf("reasoning_effort = %v, want %q", result["reasoning_effort"], effort)
			}
		})
	}
}
