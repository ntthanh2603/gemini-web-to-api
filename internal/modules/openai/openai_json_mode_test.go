package openai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gemini-web-to-api/internal/modules/openai/dto"
	"gemini-web-to-api/internal/modules/providers"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const personSchema = `{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name","age"]}`

const personJSON = `{"name":"Ada","age":36}`

const jsonSchemaPersonRequestJSON = `{
	"model": "gemini-3.6-flash",
	"messages": [{"role": "user", "content": "Invent a person."}],
	"response_format": {"type": "json_schema", "json_schema": {"name": "person", "strict": true, "schema": ` + personSchema + `}}
}`

const jsonObjectRequestJSON = `{
	"model": "gemini-3.6-flash",
	"messages": [{"role": "user", "content": "Invent a person."}],
	"response_format": {"type": "json_object"}
}`

func jsonModeRequest(t *testing.T, body string) dto.ChatCompletionRequest {
	t.Helper()
	var req dto.ChatCompletionRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func newJSONModeTestService(t *testing.T, fake *fakeGemini, log *zap.Logger) *OpenAIService {
	t.Helper()
	client := providers.NewVideoTestClient([]providers.ModelInfo{{ID: "gemini-3.6-flash"}}, nil)
	t.Cleanup(func() { _ = client.Close() })
	service := NewOpenAIService(client, log)
	service.generate = fake.generate
	return service
}

func TestExtractJSONReply(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "bare object", text: personJSON, want: personJSON},
		{name: "json fence", text: "```json\n" + personJSON + "\n```", want: personJSON},
		{name: "unlabelled fence", text: "```\n" + personJSON + "\n```", want: personJSON},
		{name: "prose around the object", text: "Here is the person:\n" + personJSON + "\nHope it helps.", want: personJSON},
		{name: "prose with braces before the object", text: "Picking {name, age}:\n" + personJSON, want: personJSON},
		{name: "fenced object with prose around the fence", text: "Sure.\n```json\n" + personJSON + "\n```\nDone.", want: personJSON},
		{name: "raw newline inside a string", text: "{\"name\":\"Ada\nLovelace\",\"age\":36}", want: "{\"name\":\"Ada\\nLovelace\",\"age\":36}"},
		{
			name: "raw newline next to a nested object repairs the whole reply",
			text: "{\"name\":\"Ada\",\"meta\":{\"a\":1},\"bio\":\"line1\nline2\"}",
			want: "{\"name\":\"Ada\",\"meta\":{\"a\":1},\"bio\":\"line1\\nline2\"}",
		},
		{name: "broken object never yields a nested one", text: `{"person":{"name":"Ada","age":36},}`, want: ""},
		{name: "truncated object never yields a nested one", text: `{"person":{"name":"Ada","age":36},"bio":"Mathem`, want: ""},
		{name: "not JSON", text: "The answer is 42.", want: ""},
		{name: "truncated object", text: `{"name":"Ada"`, want: ""},
		{name: "empty", text: "   ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractJSONReply(tt.text); got != tt.want {
				t.Errorf("extractJSONReply(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestValidateJSONReply(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		schema  json.RawMessage
		want    string
		wantErr string
	}{
		{name: "json_object accepts an object", text: personJSON, want: personJSON},
		{name: "json_object accepts a fenced object", text: "```json\n" + personJSON + "\n```", want: personJSON},
		{name: "json_object accepts a prose-wrapped object", text: "Here: " + personJSON, want: personJSON},
		{name: "json_object rejects an array", text: `[{"name":"Ada"}]`, wantErr: "not a JSON object"},
		{name: "json_object rejects a scalar", text: `"Ada"`, wantErr: "not a JSON object"},
		{name: "json_object rejects prose", text: "The answer is 42.", wantErr: "not valid JSON"},
		{
			name:   "json_schema accepts the required keys",
			text:   personJSON,
			schema: json.RawMessage(personSchema),
			want:   personJSON,
		},
		{
			name:    "json_schema rejects a missing required key",
			text:    `{"name":"Ada"}`,
			schema:  json.RawMessage(personSchema),
			wantErr: `missing required key "age"`,
		},
		{
			name:    "json_schema rejects the wrong top-level type",
			text:    `["Ada"]`,
			schema:  json.RawMessage(personSchema),
			wantErr: "want object",
		},
		{
			name:   "json_schema accepts an array when it asks for one",
			text:   `[{"name":"Ada","age":36}]`,
			schema: json.RawMessage(`{"type":"array","items":{"type":"object"}}`),
			want:   `[{"name":"Ada","age":36}]`,
		},
		{
			name:   "unreadable schema only needs valid JSON",
			text:   `["anything"]`,
			schema: json.RawMessage(`"not a schema"`),
			want:   `["anything"]`,
		},
		{
			name:    "invalid JSON fails before the schema is checked",
			text:    "no json here",
			schema:  json.RawMessage(personSchema),
			wantErr: "not valid JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateJSONReply(tt.text, tt.schema)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got %q err=%v, want error containing %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildJSONModePrompt(t *testing.T) {
	base := "Conversation:\nuser: Invent a person."

	objectPrompt := buildJSONModePrompt(base, nil)
	if !strings.Contains(objectPrompt, "JSON only") || !strings.Contains(objectPrompt, "single JSON object") {
		t.Errorf("json_object prompt does not ask for a JSON object:\n%s", objectPrompt)
	}
	if !strings.Contains(objectPrompt, base) {
		t.Errorf("json_object prompt dropped the conversation:\n%s", objectPrompt)
	}
	if strings.Contains(objectPrompt, `{"answer"`) {
		t.Errorf("json_object prompt must not suggest a shape the model copies:\n%s", objectPrompt)
	}

	schemaPrompt := buildJSONModePrompt(base, json.RawMessage(personSchema))
	if !strings.Contains(schemaPrompt, personSchema) || !strings.Contains(schemaPrompt, "required") {
		t.Errorf("json_schema prompt does not carry the schema:\n%s", schemaPrompt)
	}
}

func TestCreateChatCompletionWithoutResponseFormatKeepsPlainText(t *testing.T) {
	fake := &fakeGemini{replies: []string{"The answer is 42."}}
	service := newJSONModeTestService(t, fake, zap.NewNop())

	req := jsonModeRequest(t, `{
		"model": "gemini-3.6-flash",
		"messages": [{"role": "user", "content": "Invent a person."}],
		"response_format": {"type": "text"}
	}`)
	resp, err := service.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.prompts) != 1 {
		t.Errorf("Gemini was called %d times, want 1", len(fake.prompts))
	}
	if got := resp.Choices[0].Message.Content; got != "The answer is 42." {
		t.Errorf("content = %q, want the plain reply", got)
	}
}

func TestCreateChatCompletionJSONModeKeepsValidReply(t *testing.T) {
	fake := &fakeGemini{replies: []string{"Invented:\n```json\n" + personJSON + "\n```"}}
	service := newJSONModeTestService(t, fake, zap.NewNop())

	resp, err := service.CreateChatCompletion(context.Background(), jsonModeRequest(t, jsonSchemaPersonRequestJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.prompts) != 1 {
		t.Errorf("Gemini was called %d times, want 1", len(fake.prompts))
	}
	if got := resp.Choices[0].Message.Content; got != personJSON {
		t.Errorf("content = %q, want %q", got, personJSON)
	}
	if !strings.Contains(fake.prompts[0], "JSON only") {
		t.Errorf("first prompt does not ask for JSON:\n%s", fake.prompts[0])
	}
}

func TestCreateChatCompletionJSONModeRepairsInvalidReply(t *testing.T) {
	fake := &fakeGemini{replies: []string{
		"Here is the person: {\"name\":\"Ada\"}",
		"```json\n" + personJSON + "\n```",
	}}
	service := newJSONModeTestService(t, fake, zap.NewNop())

	resp, err := service.CreateChatCompletion(context.Background(), jsonModeRequest(t, jsonSchemaPersonRequestJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.prompts) != 2 {
		t.Fatalf("Gemini was called %d times, want 2", len(fake.prompts))
	}
	repair := fake.prompts[1]
	if !strings.Contains(repair, "Your previous reply could not be used") || !strings.Contains(repair, `required key "age"`) {
		t.Errorf("repair prompt does not explain the failure:\n%s", repair)
	}
	if !strings.Contains(repair, "Invent a person.") {
		t.Errorf("repair prompt dropped the conversation:\n%s", repair)
	}
	if got := resp.Choices[0].Message.Content; got != personJSON {
		t.Errorf("content = %q, want %q", got, personJSON)
	}
}

func TestCreateChatCompletionJSONModeReturnsRawReplyAfterOneRepair(t *testing.T) {
	fake := &fakeGemini{replies: []string{
		"Sure! Here is the person you asked for.",
		"Still not JSON, sorry.",
	}}
	core, logs := observer.New(zap.WarnLevel)
	service := newJSONModeTestService(t, fake, zap.New(core))

	resp, err := service.CreateChatCompletion(context.Background(), jsonModeRequest(t, jsonObjectRequestJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.prompts) != 2 {
		t.Fatalf("Gemini was called %d times, want 2 (one reply, one repair)", len(fake.prompts))
	}
	if got := resp.Choices[0].Message.Content; got != "Sure! Here is the person you asked for." {
		t.Errorf("content = %q, want the raw first reply", got)
	}
	if logs.FilterMessage("Gemini reply still does not match response_format after one repair attempt; returning the raw reply").Len() != 1 {
		t.Errorf("expected one warning about the unusable reply, got %v", logs.All())
	}
}

func TestCreateChatCompletionJSONModeSkipsToolCalls(t *testing.T) {
	fake := &fakeGemini{replies: []string{
		`{"tool_calls":[{"name":"done","arguments":` + wantDoneArguments + `}]}`,
	}}
	service := newJSONModeTestService(t, fake, zap.NewNop())

	req := doneToolRequest(t, forcedDone)
	req.ResponseFormat = &dto.ResponseFormat{Type: "json_object"}

	resp, err := service.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.prompts) != 1 {
		t.Fatalf("Gemini was called %d times, want 1: a tool call must not be re-asked for JSON", len(fake.prompts))
	}
	choice := resp.Choices[0]
	if choice.FinishReason != "tool_calls" || len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("got finish_reason=%q tool_calls=%v", choice.FinishReason, choice.Message.ToolCalls)
	}
	if choice.Message.Content != "" {
		t.Errorf("content = %q, want empty", choice.Message.Content)
	}
	if !strings.Contains(fake.prompts[0], `"content" must be the reply itself as a JSON object (not a string)`) {
		t.Errorf("tool bridge prompt does not ask for JSON content:\n%s", fake.prompts[0])
	}
}

func TestCreateChatCompletionJSONModeRepairsToolBridgeContent(t *testing.T) {
	fake := &fakeGemini{replies: []string{
		`{"content":"Sure, here you go."}`,
		`{"content":"{\"ok\":true}"}`,
	}}
	service := newJSONModeTestService(t, fake, zap.NewNop())

	req := doneToolRequest(t, `"auto"`)
	req.ResponseFormat = &dto.ResponseFormat{Type: "json_object"}

	resp, err := service.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.prompts) != 2 {
		t.Fatalf("Gemini was called %d times, want 2", len(fake.prompts))
	}
	if got := resp.Choices[0].Message.Content; got != `{"ok":true}` {
		t.Errorf("content = %q, want the repaired JSON", got)
	}
}

func TestCreateChatCompletionJSONModeAcceptsObjectToolBridgeContent(t *testing.T) {
	for _, format := range []dto.ResponseFormat{
		{Type: "json_object"},
		{Type: "json_schema", JSONSchema: json.RawMessage(`{"name":"person","schema":` + personSchema + `}`)},
	} {
		t.Run(format.Type, func(t *testing.T) {
			// The bridge rule asks for the JSON reply itself in "content".
			fake := &fakeGemini{replies: []string{`{"content":` + personJSON + `}`}}
			service := newJSONModeTestService(t, fake, zap.NewNop())

			req := doneToolRequest(t, `"auto"`)
			req.ResponseFormat = &format

			resp, err := service.CreateChatCompletion(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(fake.prompts) != 1 {
				t.Fatalf("Gemini was called %d times, want 1", len(fake.prompts))
			}
			if got := resp.Choices[0].Message.Content; got != personJSON {
				t.Errorf("content = %q, want the object from the bridge content, unwrapped", got)
			}
		})
	}
}

func TestCreateChatCompletionJSONModeToolBridgeRepairIsOneRequest(t *testing.T) {
	fake := &fakeGemini{replies: []string{
		`{"content":"Sure, here you go."}`,
		// An unusable tool call: generateToolBridge would retry it.
		`{"tool_calls":[{"name":"done","arguments":{}}]}`,
	}}
	service := newJSONModeTestService(t, fake, zap.NewNop())

	req := doneToolRequest(t, `"auto"`)
	req.ResponseFormat = &dto.ResponseFormat{Type: "json_object"}

	resp, err := service.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.prompts) != 2 {
		t.Fatalf("Gemini was called %d times, want 2: the repair is a single request", len(fake.prompts))
	}
	if got := resp.Choices[0].Message.Content; got != "Sure, here you go." {
		t.Errorf("content = %q, want the raw reply after a failed repair", got)
	}
}

func TestCreateChatCompletionStreamJSONModeStreamsCleanJSON(t *testing.T) {
	fake := &fakeGemini{replies: []string{"Here you go:\n```json\n" + personJSON + "\n```"}}
	service := newJSONModeTestService(t, fake, zap.NewNop())

	var streamed strings.Builder
	finishReason := ""
	err := service.CreateChatCompletionStream(context.Background(), jsonModeRequest(t, jsonSchemaPersonRequestJSON),
		func(chunk dto.ChatCompletionChunk) bool {
			for _, choice := range chunk.Choices {
				streamed.WriteString(choice.Delta.Content)
				if choice.FinishReason != "" {
					finishReason = choice.FinishReason
				}
			}
			return true
		})
	if err != nil {
		t.Fatal(err)
	}
	if got := streamed.String(); got != personJSON {
		t.Errorf("streamed content = %q, want %q", got, personJSON)
	}
	if finishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", finishReason)
	}
}
