package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gemini-web-to-api/internal/commons/utils"
	"gemini-web-to-api/internal/modules/openai/dto"
	"gemini-web-to-api/internal/modules/providers"
)

func TestChatCompletionUsageEstimatesWhenWireHasNone(t *testing.T) {
	service := newToolBridgeTestService(t, &fakeGemini{replies: []string{"Xin chào thế giới"}})
	req := dto.ChatCompletionRequest{
		Model:    "gemini-3.6-flash",
		Messages: []dto.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
	}
	resp, err := service.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	prompt, completion := utils.EstimateTokens("User: Hello"), utils.EstimateTokens("Xin chào thế giới")
	if resp.Usage.PromptTokens != prompt || resp.Usage.CompletionTokens != completion ||
		resp.Usage.TotalTokens != prompt+completion {
		t.Fatalf("usage = %+v, want %d/%d/%d", resp.Usage, prompt, completion, prompt+completion)
	}
}

func TestChatCompletionUsageCountsReasoning(t *testing.T) {
	service := newToolBridgeTestService(t, &fakeGemini{})
	service.generate = func(context.Context, string, ...providers.GenerateOption) (*providers.Response, error) {
		return &providers.Response{Text: "42", ReasoningText: "Six times seven is forty-two."}, nil
	}
	req := dto.ChatCompletionRequest{
		Model:    "gemini-3.6-flash",
		Messages: []dto.ChatCompletionMessage{{Role: "user", Content: "6*7?"}},
	}
	resp, err := service.CreateChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := utils.EstimateTokens("42") + utils.EstimateTokens("Six times seven is forty-two.")
	if resp.Usage.CompletionTokens != want {
		t.Fatalf("completion_tokens = %d, want %d (answer + reasoning)", resp.Usage.CompletionTokens, want)
	}
}

func TestChatCompletionStreamUsageChunk(t *testing.T) {
	tests := []struct {
		name         string
		reply        string
		tools        bool
		includeUsage bool
	}{
		{name: "text without include_usage", reply: "Hi there"},
		{name: "text with include_usage", reply: "Hi there", includeUsage: true},
		{name: "tool call with include_usage", reply: `{"tool_calls":[{"name":"done","arguments":{"answer":"Done.","document":{"title":"T","body":"B"}}}]}`, tools: true, includeUsage: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newToolBridgeTestService(t, &fakeGemini{replies: []string{tt.reply}})
			req := dto.ChatCompletionRequest{
				Model:    "gemini-3.6-flash",
				Messages: []dto.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
			}
			if tt.tools {
				req = doneToolRequest(t, forcedDone)
			}
			req.Stream = true
			req.StreamOptions = &dto.StreamOptions{IncludeUsage: tt.includeUsage}

			var chunks []dto.ChatCompletionChunk
			err := service.CreateChatCompletionStream(context.Background(), req, func(chunk dto.ChatCompletionChunk) bool {
				chunks = append(chunks, chunk)
				return true
			})
			if err != nil {
				t.Fatal(err)
			}

			last := chunks[len(chunks)-1]
			if !tt.includeUsage {
				for _, chunk := range chunks {
					if chunk.Usage != nil {
						t.Fatalf("usage sent without stream_options.include_usage: %+v", chunk)
					}
				}
				return
			}
			if last.Usage == nil || last.Usage.TotalTokens == 0 {
				t.Fatalf("last chunk has no usage: %+v", last)
			}
			raw, _ := json.Marshal(last)
			if !strings.Contains(string(raw), `"choices":[]`) {
				t.Fatalf("usage chunk must have empty choices, got %s", raw)
			}
			beforeLast := chunks[len(chunks)-2]
			if len(beforeLast.Choices) != 1 || beforeLast.Choices[0].FinishReason == "" {
				t.Fatalf("finish_reason chunk must precede the usage chunk, got %s", fmt.Sprint(beforeLast))
			}
		})
	}
}
