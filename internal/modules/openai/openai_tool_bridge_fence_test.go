package openai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gemini-web-to-api/internal/modules/openai/dto"

	"go.uber.org/zap"
)

const patchToolRequestJSON = `{
	"model": "gemini-3.6-flash",
	"messages": [{"role":"user","content":"Add compact to the CountdownTimer in page.tsx."}],
	"tools": [{"type":"function","function":{"name":"patch","description":"Replace old_string with new_string in a file",
		"parameters":{"type":"object",
			"properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"}},
			"required":["path","old_string","new_string"]}}}]
}`

// corruptedJSXReply is what Gemini web returns for an unfenced patch of a JSX
// line: the tag's attributes have been re-quoted, so the JSON has unescaped
// double quotes inside a string.
const corruptedJSXReply = `{"tool_calls":[{"name":"patch","arguments":{"path":"/src/page.tsx",` +
	`"old_string":"<CountdownTimer createdAt="{rem.createdAt}" days="{rem.days}"/>",` +
	`"new_string":"<CountdownTimer createdAt="{rem.createdAt}" days="{rem.days}" compact/>"}}]}`

const wantPatchArguments = `{"path":"/src/page.tsx",` +
	`"old_string":"<CountdownTimer createdAt={rem.createdAt} days={rem.days} />",` +
	`"new_string":"<CountdownTimer createdAt={rem.createdAt} days={rem.days} compact />"}`

const fencedJSXReply = "```json\n{\"tool_calls\":[{\"name\":\"patch\",\"arguments\":" + wantPatchArguments + "}]}\n```"

func patchToolRequest(t *testing.T) dto.ChatCompletionRequest {
	t.Helper()
	var req dto.ChatCompletionRequest
	if err := json.Unmarshal([]byte(patchToolRequestJSON), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return req
}

// A tool call that is not valid JSON must be an error, never assistant text,
// or the client shows the raw JSON to the user and no tool runs.
func TestToolBridgeRejectsMalformedToolCall(t *testing.T) {
	service := &OpenAIService{log: zap.NewNop()}

	calls, content, err := service.parseToolBridgeOutput(patchToolRequest(t), corruptedJSXReply)
	if err == nil {
		t.Fatalf("expected an error, got calls=%v content=%q", calls, content)
	}
	if content != "" || len(calls) != 0 {
		t.Errorf("malformed tool call leaked: calls=%v content=%q", calls, content)
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("error %q does not say the JSON is invalid", err)
	}
}

func TestToolBridgeParsesFencedJSXArguments(t *testing.T) {
	service := &OpenAIService{log: zap.NewNop()}

	calls, _, err := service.parseToolBridgeOutput(patchToolRequest(t), fencedJSXReply)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(calls) != 1 || calls[0].Function.Name != "patch" {
		t.Fatalf("expected one patch call, got %+v", calls)
	}
	if calls[0].Function.Arguments != wantPatchArguments {
		t.Errorf("arguments changed:\n got %s\nwant %s", calls[0].Function.Arguments, wantPatchArguments)
	}
}

// Prose that is not bridge JSON and has no tool_calls key is still a reply.
func TestToolBridgeKeepsPlainTextReply(t *testing.T) {
	service := &OpenAIService{log: zap.NewNop()}

	calls, content, err := service.parseToolBridgeOutput(patchToolRequest(t), "All done, the file is updated.")
	if err != nil || len(calls) != 0 || content != "All done, the file is updated." {
		t.Errorf("got calls=%v content=%q err=%v", calls, content, err)
	}
}

func TestToolBridgeRetriesMalformedToolCall(t *testing.T) {
	fake := &fakeGemini{replies: []string{corruptedJSXReply, fencedJSXReply}}
	service := newToolBridgeTestService(t, fake)
	req := patchToolRequest(t)

	_, calls, _, err := service.generateToolBridge(context.Background(), req, "PROMPT", nil)
	if err != nil {
		t.Fatalf("generateToolBridge: %v", err)
	}
	if len(fake.prompts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(fake.prompts))
	}
	if !strings.Contains(fake.prompts[1], "not valid JSON") || !strings.Contains(fake.prompts[1], "```json code fence") {
		t.Errorf("retry prompt does not explain the problem: %q", fake.prompts[1])
	}
	if len(calls) != 1 || calls[0].Function.Arguments != wantPatchArguments {
		t.Errorf("unexpected calls after retry: %+v", calls)
	}
}

// Gemini web re-quotes JSX attributes in unfenced text, so the prompt must ask
// for the JSON inside a code fence, both before and after the conversation.
func TestBuildToolBridgePromptAsksForCodeFence(t *testing.T) {
	s := &OpenAIService{}
	base := "User: Add compact to the CountdownTimer in page.tsx."
	prompt := s.buildToolBridgePrompt(patchToolRequest(t), base)

	if strings.Contains(prompt, "no markdown code fences") {
		t.Error("prompt still forbids code fences")
	}
	head, tail, found := strings.Cut(prompt, base)
	if !found {
		t.Fatal("prompt does not contain the conversation")
	}
	for name, part := range map[string]string{"header": head, "final rule": tail} {
		if !strings.Contains(part, "```json code fence") {
			t.Errorf("%s does not ask for a ```json code fence", name)
		}
	}
}
