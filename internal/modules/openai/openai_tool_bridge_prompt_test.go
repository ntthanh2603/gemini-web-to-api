package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"gemini-web-to-api/internal/modules/openai/dto"
)

// Gemini web refuses a prompt that claims it is an API or has tools, so the
// bridge prompt must stay in the third person and end with the output rule.
func TestBuildToolBridgePromptUsesAgentFraming(t *testing.T) {
	var req dto.ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{
		"model": "gemini-quick-answers",
		"messages": [{"role": "user", "content": "Check uptime"}],
		"tools": [{"type": "function", "function": {"name": "terminal", "description": "Run a shell command",
			"parameters": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}}}]
	}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	s := &OpenAIService{}
	base := "User: Check uptime"
	prompt := s.buildToolBridgePrompt(req, base)

	for _, banned := range []string{"OpenAI-compatible assistant", "running behind a bridge"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("prompt contains %q, which Gemini web rejects", banned)
		}
	}
	for _, want := range []string{"The program, not you, executes", "- name: terminal", `"required": ["command"]`, base} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
	if strings.Index(prompt, base) > strings.LastIndex(prompt, "Write the agent's next message now") {
		t.Error("the output rule must come after the conversation")
	}
}
