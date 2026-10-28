package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gemini-web-to-api/internal/commons/models"
	common "gemini-web-to-api/internal/commons/utils"
	"gemini-web-to-api/internal/modules/claude/dto"
	"gemini-web-to-api/internal/modules/providers"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type ClaudeService struct {
	client *providers.Client
	log    *zap.Logger
}

func NewClaudeService(client *providers.Client, log *zap.Logger) *ClaudeService {
	return &ClaudeService{
		client: client,
		log:    log,
	}
}

func (s *ClaudeService) ListModels() []providers.ModelInfo {
	return s.client.ListModels()
}

func (s *ClaudeService) ResolveModel(model string) (providers.ModelInfo, error) {
	return s.client.ResolveModel(model)
}

func (s *ClaudeService) ValidateMessage(req dto.MessageRequest) error {
	if err := common.ValidateMessages(req.Messages); err != nil {
		return err
	}
	_, err := s.client.ResolveModel(req.Model)
	return err
}

func (s *ClaudeService) GenerateMessage(ctx context.Context, req dto.MessageRequest) (*dto.MessageResponse, error) {
	// Logic: Validate
	if err := s.ValidateMessage(req); err != nil {
		return nil, err
	}

	// Logic: Build Prompt
	prompt := s.buildPrompt(req)
	if prompt == "" {
		return nil, fmt.Errorf("no valid content in messages")
	}
	hasTools := len(req.Tools) > 0

	opts := []providers.GenerateOption{}
	if req.Model != "" {
		opts = append(opts, providers.WithModel(req.Model))
	}
	inputFiles, err := providers.InputFilesFromAttachments(req.Messages)
	if err != nil {
		return nil, err
	}
	if len(inputFiles) > 0 {
		opts = append(opts, providers.WithInputFiles(inputFiles))
	}

	// Logic: Call Provider
	response, err := s.client.GenerateContent(ctx, prompt, opts...)
	if err != nil {
		return nil, err
	}

	// Logic: Construct Response
	msgID := fmt.Sprintf("msg_%s", uuid.New().String())
	resContent := []dto.ConfigContent{}
	stopReason := "end_turn"

	if response.ReasoningText != "" {
		resContent = append(resContent, dto.ConfigContent{Type: "thinking", Thinking: response.ReasoningText})
	}

	if hasTools {
		toolUses, text := s.parseToolBridgeOutput(req, response.Text)
		if len(toolUses) > 0 {
			for _, tu := range toolUses {
				resContent = append(resContent, tu)
			}
			stopReason = "tool_use"
		} else {
			resContent = append(resContent, dto.ConfigContent{Type: "text", Text: text})
		}
	} else {
		resContent = append(resContent, dto.ConfigContent{Type: "text", Text: response.Text})
	}

	tokens := response.CountTokens(prompt)

	return &dto.MessageResponse{
		ID:         msgID,
		Type:       "message",
		Role:       "assistant",
		Model:      response.Model,
		Content:    resContent,
		StopReason: stopReason,
		Usage:      dto.Usage{InputTokens: tokens.PromptTokens, OutputTokens: tokens.CompletionTokens},
		Canvases:   providers.CanvasFiles(response.Canvases),
	}, nil
}

// CountTokens estimates the input tokens of req, counted on the same prompt
// GenerateMessage sends, so it agrees with the reported usage.input_tokens.
func (s *ClaudeService) CountTokens(req dto.MessageRequest) int {
	return common.EstimateTokens(s.buildPrompt(req))
}

// buildPrompt builds the prompt sent to Gemini for req, wrapped in the tool
// bridge when req has tools. It is empty when the messages have no content.
func (s *ClaudeService) buildPrompt(req dto.MessageRequest) string {
	prompt := common.BuildPromptFromMessages(req.Messages, req.System.Text)
	if prompt != "" && len(req.Tools) > 0 {
		prompt = s.buildToolBridgePrompt(req, prompt)
	}
	return prompt
}

// GenerateMessageStream handles the logic of transforming a message generation into a stream of Claude events.
func (s *ClaudeService) GenerateMessageStream(ctx context.Context, req dto.MessageRequest, onEvent func(dto.StreamEvent) bool) error {
	response, err := s.GenerateMessage(ctx, req)
	if err != nil {
		return err
	}
	return s.streamMessage(ctx, response, onEvent)
}

// streamMessage replays a finished message as Claude stream events.
func (s *ClaudeService) streamMessage(ctx context.Context, response *dto.MessageResponse, onEvent func(dto.StreamEvent) bool) error {
	// message_start
	if !onEvent(dto.StreamEvent{
		Type: "message_start",
		Message: &dto.MessageResponse{
			ID:       response.ID,
			Type:     "message",
			Role:     "assistant",
			Model:    response.Model,
			Usage:    response.Usage,
			Canvases: response.Canvases,
		},
	}) {
		return nil
	}

	for i, content := range response.Content {
		// content_block_start
		startEv := dto.StreamEvent{
			Type:         "content_block_start",
			Index:        i,
			ContentBlock: &dto.ConfigContent{Type: content.Type},
		}
		if content.Type == "tool_use" {
			startEv.ContentBlock.ID = content.ID
			startEv.ContentBlock.Name = content.Name
		}
		if !onEvent(startEv) {
			return nil
		}

		if content.Type == "thinking" {
			chunks := common.SplitResponseIntoChunks(content.Thinking, 30)
			for _, chunk := range chunks {
				if !onEvent(dto.StreamEvent{
					Type:  "content_block_delta",
					Index: i,
					DeltaField: &models.Delta{
						Type:     "thinking_delta",
						Thinking: chunk,
					},
				}) {
					return nil
				}
				if !common.SleepWithCancel(ctx, 30*time.Millisecond) {
					return nil
				}
			}
		} else if content.Type == "text" {
			chunks := common.SplitResponseIntoChunks(content.Text, 30)
			for _, chunk := range chunks {
				if !onEvent(dto.StreamEvent{
					Type:  "content_block_delta",
					Index: i,
					DeltaField: &models.Delta{
						Type: "text_delta",
						Text: chunk,
					},
				}) {
					return nil
				}
				if !common.SleepWithCancel(ctx, 30*time.Millisecond) {
					return nil
				}
			}
		} else if content.Type == "tool_use" {
			inputJSON, err := json.Marshal(content.Input)
			if err != nil {
				s.log.Error("Failed to marshal tool input", zap.Error(err))
				return fmt.Errorf("failed to marshal tool input: %w", err)
			}
			if !onEvent(dto.StreamEvent{
				Type:  "content_block_delta",
				Index: i,
				DeltaField: &models.Delta{
					Type:        "input_json_delta",
					PartialJSON: string(inputJSON),
				},
			}) {
				return nil
			}
		}

		// content_block_stop
		if !onEvent(dto.StreamEvent{
			Type:  "content_block_stop",
			Index: i,
		}) {
			return nil
		}
	}

	// message_delta carries the final usage, as Anthropic streams do.
	if !onEvent(dto.StreamEvent{
		Type: "message_delta",
		DeltaField: &models.Delta{
			StopReason: response.StopReason,
		},
		UsageField: &response.Usage,
	}) {
		return nil
	}

	// message_stop
	onEvent(dto.StreamEvent{Type: "message_stop"})
	return nil
}

func (s *ClaudeService) buildToolBridgePrompt(req dto.MessageRequest, basePrompt string) string {
	var b strings.Builder
	b.WriteString("You are a Claude-compatible assistant running behind a bridge that supports tool use.\n")
	b.WriteString("You MUST respond with JSON only. Do not output markdown code fences.\n")
	b.WriteString("Output schema:\n")
	b.WriteString("{\"status\":\"tool_use\",\"tool_calls\":[{\"id\":\"<unique_id>\",\"name\":\"<tool_name>\",\"input\":{}}]} OR {\"status\":\"text\",\"content\":\"<assistant_text>\"}\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Use only tool names listed below.\n")
	b.WriteString("- input must be valid JSON object.\n")

	b.WriteString("Available tools:\n")
	for _, t := range req.Tools {
		b.WriteString("- name: ")
		b.WriteString(t.Name)
		if t.Description != "" {
			b.WriteString(" | description: ")
			b.WriteString(t.Description)
		}
		if len(t.InputSchema) > 0 {
			b.WriteString(" | input_schema: ")
			b.Write(t.InputSchema)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nConversation:\n")
	b.WriteString(basePrompt)
	return b.String()
}

func (s *ClaudeService) parseToolBridgeOutput(req dto.MessageRequest, text string) ([]dto.ConfigContent, string) {
	cleaned := common.StripCodeFence(text)
	if cleaned == "" {
		return nil, ""
	}

	var payload struct {
		Status    string `json:"status"`
		ToolCalls []struct {
			ID    string                 `json:"id"`
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		} `json:"tool_calls"`
		Content string `json:"content"`
	}

	if err := json.Unmarshal([]byte(cleaned), &payload); err != nil {
		obj := common.ExtractFirstJSONObject(text)
		if obj == "" || json.Unmarshal([]byte(obj), &payload) != nil {
			return nil, text
		}
	}

	if payload.Status == "tool_use" && len(payload.ToolCalls) > 0 {
		uses := make([]dto.ConfigContent, 0, len(payload.ToolCalls))
		for _, tc := range payload.ToolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%s", uuid.New().String())
			}
			uses = append(uses, dto.ConfigContent{
				Type:  "tool_use",
				ID:    id,
				Name:  tc.Name,
				Input: tc.Input,
			})
		}
		return uses, ""
	}

	return nil, payload.Content
}
