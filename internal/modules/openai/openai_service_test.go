package openai

import (
	"testing"

	"gemini-web-to-api/internal/modules/openai/dto"

	"go.uber.org/zap"
)

func TestOpenAIToolBridgeParsing(t *testing.T) {
	service := &OpenAIService{log: zap.NewNop()}

	req := dto.ChatCompletionRequest{
		Tools: []dto.ToolDefinition{
			{
				Type: "function",
				Function: dto.ToolFunctionDefinition{
					Name: "get_current_weather",
				},
			},
		},
	}

	t.Run("parse valid tool call", func(t *testing.T) {
		input := "```json\n{\"status\":\"tool_use\",\"tool_calls\":[{\"name\":\"get_current_weather\",\"arguments\":{\"location\":\"Hanoi\"}}]}\n```"
		calls, content := service.parseToolBridgeOutput(req, input)
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if calls[0].Function.Name != "get_current_weather" {
			t.Errorf("got name %q, want get_current_weather", calls[0].Function.Name)
		}
		if content != "" {
			t.Errorf("got content %q, want empty", content)
		}
	})

	t.Run("parse tool call with surrounding text", func(t *testing.T) {
		input := "I'll check the weather for Hanoi right now.\n{\"status\":\"tool_use\",\"tool_calls\":[{\"name\":\"get_current_weather\",\"arguments\":{\"location\":\"Hanoi\"}}]}\nDone."
		calls, _ := service.parseToolBridgeOutput(req, input)
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if calls[0].Function.Name != "get_current_weather" {
			t.Errorf("got name %q, want get_current_weather", calls[0].Function.Name)
		}
	})

	t.Run("parse non-tool plain text", func(t *testing.T) {
		input := "The weather in Hanoi is nice and sunny today."
		calls, content := service.parseToolBridgeOutput(req, input)
		if len(calls) != 0 {
			t.Errorf("expected 0 tool calls, got %d", len(calls))
		}
		if content != input {
			t.Errorf("got content %q, want %q", content, input)
		}
	})
}
