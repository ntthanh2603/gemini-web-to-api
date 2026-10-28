package gemini

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gemini-web-to-api/internal/modules/gemini/dto"
	"gemini-web-to-api/internal/modules/providers"

	"go.uber.org/zap"
)

const weatherTools = `"tools":[{"functionDeclarations":[
	{"name":"get_weather","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}}}},
	{"name":"get_time","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}}}}]}]`

// fakeGeminiReply answers every prompt with reply and records the prompts.
type fakeGeminiReply struct {
	reply   string
	prompts []string
}

func (f *fakeGeminiReply) generate(_ context.Context, prompt string, _ ...providers.GenerateOption) (*providers.Response, error) {
	f.prompts = append(f.prompts, prompt)
	return &providers.Response{Text: f.reply}, nil
}

func toolTestService(fake *fakeGeminiReply) *GeminiService {
	return &GeminiService{log: zap.NewNop(), generate: fake.generate}
}

func decodeGenerateRequest(t *testing.T, body string) dto.GeminiGenerateRequest {
	t.Helper()
	var req dto.GeminiGenerateRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestGenerateContentCompletesAToolCycle(t *testing.T) {
	// Round 1: the model asks for the tool.
	fake := &fakeGeminiReply{reply: `{"status":"call","tool_calls":[{"name":"get_weather","arguments":{"city":"Hanoi"}}]}`}
	req := decodeGenerateRequest(t, `{"contents":[{"role":"user","parts":[{"text":"What is the weather in Hanoi?"}]}],`+weatherTools+`}`)
	resp, err := toolTestService(fake).GenerateContent(context.Background(), "gemini-3.6-flash", req)
	if err != nil {
		t.Fatal(err)
	}
	candidate := resp.Candidates[0]
	if candidate.FinishReason != "STOP" || len(candidate.Content.Parts) != 1 || candidate.Content.Parts[0].FunctionCall == nil {
		t.Fatalf("round 1 candidate = %+v, want one functionCall part and STOP", candidate)
	}
	raw, _ := json.Marshal(resp)
	if !strings.Contains(string(raw), `"functionCall":{"name":"get_weather","args":{"city":"Hanoi"}}`) {
		t.Fatalf("round 1 response = %s, want a camelCase functionCall", raw)
	}

	// Round 2: the client sends the call and its result back.
	fake = &fakeGeminiReply{reply: `{"status":"text","content":"It is 31°C and sunny in Hanoi."}`}
	req = decodeGenerateRequest(t, `{"contents":[
		{"role":"user","parts":[{"text":"What is the weather in Hanoi?"}]},
		{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Hanoi"}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"temp_c":31,"sky":"sunny"}}}]}
	],`+weatherTools+`}`)
	resp, err = toolTestService(fake).GenerateContent(context.Background(), "gemini-3.6-flash", req)
	if err != nil {
		t.Fatal(err)
	}
	prompt := fake.prompts[0]
	for _, want := range []string{
		`Model: [Call Tool: get_weather with Input: {"city":"Hanoi"}]`,
		`User: [Tool Result for get_weather]: {"temp_c":31,"sky":"sunny"}`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("round 2 prompt lacks %q:\n%s", want, prompt)
		}
	}
	if got := resp.Candidates[0].Content.Parts[0].Text; got != "It is 31°C and sunny in Hanoi." {
		t.Fatalf("round 2 text = %q, want the final answer", got)
	}
}

func TestGenerateContentAppliesToolConfig(t *testing.T) {
	question := `"contents":[{"role":"user","parts":[{"text":"What is the weather in Hanoi?"}]}],`

	t.Run("NONE offers no tools", func(t *testing.T) {
		fake := &fakeGeminiReply{reply: "It is sunny."}
		req := decodeGenerateRequest(t, `{`+question+weatherTools+`,"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`)
		resp, err := toolTestService(fake).GenerateContent(context.Background(), "gemini-3.6-flash", req)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fake.prompts[0], "Available tools") {
			t.Fatalf("mode NONE prompt still offers tools:\n%s", fake.prompts[0])
		}
		if got := resp.Candidates[0].Content.Parts[0].Text; got != "It is sunny." {
			t.Fatalf("text = %q, want the plain reply", got)
		}
	})

	t.Run("ANY requires a call", func(t *testing.T) {
		fake := &fakeGeminiReply{reply: `{"status":"call","tool_calls":[{"name":"get_weather","arguments":{}}]}`}
		req := decodeGenerateRequest(t, `{`+question+weatherTools+`,"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}}`)
		if _, err := toolTestService(fake).GenerateContent(context.Background(), "gemini-3.6-flash", req); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(fake.prompts[0], "You must return at least one tool call") {
			t.Fatalf("mode ANY prompt does not require a call:\n%s", fake.prompts[0])
		}
	})

	t.Run("allowedFunctionNames restricts the tools", func(t *testing.T) {
		fake := &fakeGeminiReply{reply: `{"status":"call","tool_calls":[{"name":"get_weather","arguments":{"city":"Hanoi"}}]}`}
		req := decodeGenerateRequest(t, `{`+question+weatherTools+
			`,"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_time"]}}}`)
		resp, err := toolTestService(fake).GenerateContent(context.Background(), "gemini-3.6-flash", req)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fake.prompts[0], "name: get_weather") || !strings.Contains(fake.prompts[0], "name: get_time") {
			t.Fatalf("prompt must list only get_time:\n%s", fake.prompts[0])
		}
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.FunctionCall != nil {
				t.Fatalf("disallowed call %q reached the client", part.FunctionCall.Name)
			}
		}
	})

	t.Run("undeclared tools are dropped", func(t *testing.T) {
		fake := &fakeGeminiReply{reply: `{"status":"call","tool_calls":[{"name":"delete_files","arguments":{}}]}`}
		req := decodeGenerateRequest(t, `{`+question+weatherTools+`}`)
		resp, err := toolTestService(fake).GenerateContent(context.Background(), "gemini-3.6-flash", req)
		if err != nil {
			t.Fatal(err)
		}
		if part := resp.Candidates[0].Content.Parts[0]; part.FunctionCall != nil {
			t.Fatalf("undeclared call %q reached the client", part.FunctionCall.Name)
		}
	})
}
