package openai

import (
	"encoding/json"
	"testing"

	"gemini-web-to-api/internal/modules/openai/dto"

	"go.uber.org/zap"
)

const terminalToolRequestJSON = `{
	"model": "gemini-3.6-flash",
	"messages": [{"role":"user","content":"Show the home directory on the build host."}],
	"tools": [{"type":"function","function":{"name":"terminal","description":"Run a shell command",
		"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}]
}`

// Gemini writes shell escapes such as \$ straight into JSON strings, which is
// not a valid JSON escape. The call must still be used, with the backslash kept.
func TestToolBridgeRepairsInvalidEscapeInArguments(t *testing.T) {
	var req dto.ChatCompletionRequest
	if err := json.Unmarshal([]byte(terminalToolRequestJSON), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	service := &OpenAIService{log: zap.NewNop()}

	reply := "```json\n" +
		`{"tool_calls":[{"name":"terminal","arguments":{"command":"ssh build-host \"bash -c 'echo \$HOME'\""}}]}` +
		"\n```"
	calls, _, err := service.parseToolBridgeOutput(req, reply)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one call, got %+v", calls)
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments are not JSON: %v", err)
	}
	want := `ssh build-host "bash -c 'echo \$HOME'"`
	if args.Command != want {
		t.Errorf("command = %q, want %q", args.Command, want)
	}
}
