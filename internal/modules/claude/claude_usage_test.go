package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	common "gemini-web-to-api/internal/commons/utils"
	"gemini-web-to-api/internal/modules/claude/dto"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

func TestStreamMessageReportsUsageInStartAndDelta(t *testing.T) {
	response := &dto.MessageResponse{
		ID:         "msg_1",
		Model:      "gemini-auto",
		Content:    []dto.ConfigContent{{Type: "text", Text: "Hi"}},
		StopReason: "end_turn",
		Usage:      dto.Usage{InputTokens: 3, OutputTokens: 1},
	}
	var events []dto.StreamEvent
	err := (&ClaudeService{log: zap.NewNop()}).streamMessage(context.Background(), response, func(ev dto.StreamEvent) bool {
		events = append(events, ev)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ev := range events {
		switch ev.Type {
		case "message_start":
			if ev.Message.Usage != response.Usage {
				t.Fatalf("message_start usage = %+v, want %+v", ev.Message.Usage, response.Usage)
			}
		case "message_delta":
			if ev.UsageField == nil || *ev.UsageField != response.Usage {
				t.Fatalf("message_delta usage = %+v, want %+v", ev.UsageField, response.Usage)
			}
			raw, _ := json.Marshal(ev)
			if !strings.Contains(string(raw), `"usage":{"input_tokens":3,"output_tokens":1}`) {
				t.Fatalf("message_delta must carry only Anthropic usage fields, got %s", raw)
			}
			return
		}
	}
	t.Fatal("no message_delta event")
}

func TestCountTokensMatchesMessagesInputTokens(t *testing.T) {
	app := fiber.New()
	NewClaudeController(&ClaudeService{log: zap.NewNop()}, zap.NewNop()).Register(app.Group("/claude/v1"))

	countTokens := func(body string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/claude/v1/messages/count_tokens", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var result struct {
			InputTokens int `json:"input_tokens"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("invalid response %s: %v", raw, err)
		}
		return result.InputTokens
	}

	plain := countTokens(`{"model":"gemini-auto","system":"Be brief.","messages":[{"role":"user","content":"Xin chào"}]}`)
	if want := common.EstimateTokens("System: Be brief.\n\nUser: Xin chào"); plain != want {
		t.Fatalf("input_tokens = %d, want %d (the prompt GenerateMessage sends)", plain, want)
	}

	withTools := countTokens(`{"model":"gemini-auto","system":"Be brief.","messages":[{"role":"user","content":"Xin chào"}],
		"tools":[{"name":"lookup","description":"Look something up","input_schema":{"type":"object"}}]}`)
	if withTools <= plain {
		t.Fatalf("tool definitions must count toward input_tokens: %d with tools, %d without", withTools, plain)
	}
}
