package claude

import (
	"bytes"
	"bufio"
	"strings"
	"testing"

	common "gemini-web-to-api/internal/commons/utils"
	"gemini-web-to-api/internal/modules/claude/dto"

	"go.uber.org/zap"
)

func TestParseToolBridgeOutput(t *testing.T) {
	service := &ClaudeService{log: zap.NewNop()}
	req := dto.MessageRequest{}

	t.Run("extracts tool_use from fenced json", func(t *testing.T) {
		input := "```json\n{\"status\":\"tool_use\",\"tool_calls\":[{\"id\":\"tool_1\",\"name\":\"calculator\",\"input\":{\"expr\":\"2+2\"}}]}\n```"
		uses, content := service.parseToolBridgeOutput(req, input)
		if len(uses) != 1 {
			t.Fatalf("expected 1 tool use, got %d", len(uses))
		}
		if uses[0].Name != "calculator" || uses[0].ID != "tool_1" {
			t.Errorf("unexpected tool use: %+v", uses[0])
		}
		if content != "" {
			t.Errorf("expected empty content, got %q", content)
		}
	})

	t.Run("extracts tool_use with surrounding commentary", func(t *testing.T) {
		input := "Sure, I can calculate that for you!\n{\"status\":\"tool_use\",\"tool_calls\":[{\"name\":\"calculator\",\"input\":{\"expr\":\"10*5\"}}]}\nHope this helps!"
		uses, _ := service.parseToolBridgeOutput(req, input)
		if len(uses) != 1 {
			t.Fatalf("expected 1 tool use, got %d", len(uses))
		}
		if uses[0].Name != "calculator" {
			t.Errorf("unexpected tool use name: %q", uses[0].Name)
		}
		if !strings.HasPrefix(uses[0].ID, "toolu_") {
			t.Errorf("expected generated tool ID starting with toolu_, got %q", uses[0].ID)
		}
	})

	t.Run("falls back to raw text if not tool call", func(t *testing.T) {
		input := "Just a normal conversational answer."
		uses, content := service.parseToolBridgeOutput(req, input)
		if len(uses) != 0 {
			t.Errorf("expected 0 tool uses, got %d", len(uses))
		}
		if content != input {
			t.Errorf("got content=%q, want=%q", content, input)
		}
	})
}

func TestClaudeSSEChunkFormat(t *testing.T) {
	var buf bytes.Buffer
	writer := bufio.NewWriter(&buf)
	log := zap.NewNop()

	ev := dto.StreamEvent{
		Type: "content_block_delta",
		Index: 0,
	}

	err := common.SendSSEChunk(writer, log, ev.Type, ev)
	if err != nil {
		t.Fatalf("SendSSEChunk failed: %v", err)
	}

	result := buf.String()
	if !strings.HasPrefix(result, "event: content_block_delta\n") {
		t.Errorf("expected event: content_block_delta header, got: %s", result)
	}
	if !strings.Contains(result, "data: {") {
		t.Errorf("expected data payload, got: %s", result)
	}
	if !strings.HasSuffix(result, "\n\n") {
		t.Errorf("expected trailing double newline, got: %q", result)
	}
}
