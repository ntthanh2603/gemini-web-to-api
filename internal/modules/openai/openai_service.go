package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gemini-web-to-api/internal/commons/models"
	"gemini-web-to-api/internal/commons/utils"
	"gemini-web-to-api/internal/modules/openai/dto"
	"gemini-web-to-api/internal/modules/providers"

	"go.uber.org/zap"
)

var imagePlaceholderRegex = regexp.MustCompile(`(?i)https?://googleusercontent\.com/image_generation_content/\d+`)

type OpenAIService struct {
	client *providers.Client
	log    *zap.Logger
}

func NewOpenAIService(client *providers.Client, log *zap.Logger) *OpenAIService {
	return &OpenAIService{
		client: client,
		log:    log,
	}
}

func (s *OpenAIService) ListModels() []providers.ModelInfo {
	return s.client.ListModels()
}

func (s *OpenAIService) ResolveModel(model string) (providers.ModelInfo, error) {
	return s.client.ResolveModel(model)
}

// ValidateChatCompletion performs checks that must finish before an HTTP
// streaming response is opened.
func (s *OpenAIService) ValidateChatCompletion(req dto.ChatCompletionRequest) error {
	if err := utils.ValidateMessages(req.ToModelMessages()); err != nil {
		return err
	}
	if err := utils.ValidateGenerationRequest(req.Model, req.MaxTokens, req.Temperature); err != nil {
		return err
	}
	_, err := s.client.ResolveModel(req.Model)
	return err
}

func (s *OpenAIService) CreateChatCompletion(ctx context.Context, req dto.ChatCompletionRequest) (*dto.ChatCompletionResponse, error) {
	modelMessages := req.ToModelMessages()

	if err := s.ValidateChatCompletion(req); err != nil {
		return nil, err
	}

	// Logic: Build Prompt
	prompt := utils.BuildPromptFromMessages(modelMessages, "")
	if prompt == "" {
		return nil, fmt.Errorf("no valid content in messages")
	}

	if req.HasToolsEnabled() {
		prompt = s.buildToolBridgePrompt(req, prompt)
	}

	opts := []providers.GenerateOption{}
	if req.Model != "" {
		opts = append(opts, providers.WithModel(req.Model))
	}
	inputFiles, err := providers.InputFilesFromAttachments(modelMessages)
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

	message := dto.ChatCompletionResponseMessage{Role: "assistant"}
	finishReason := "stop"

	if req.HasToolsEnabled() {
		toolCalls, content := s.parseToolBridgeOutput(req, response.Text)
		if len(toolCalls) == 0 {
			fallback := s.buildFallbackToolCalls(req)
			if len(fallback) > 0 && (req.ToolChoiceMode() == "required" || req.ToolChoiceMode() == "function") {
				toolCalls = fallback
			}
		}

		if len(toolCalls) > 0 {
			message.ToolCalls = toolCalls
			finishReason = "tool_calls"
		} else {
			message.Content = content
		}
	} else {
		content := imagePlaceholderRegex.ReplaceAllString(response.Text, "")
		content = strings.TrimSpace(content)
		if len(response.Images) > 0 {
			var imgMarkdowns []string
			for _, img := range response.Images {
				imgMarkdowns = append(imgMarkdowns, fmt.Sprintf("![image](%s)", img.URL))
			}
			if content != "" {
				content += "\n\n"
			}
			content += strings.Join(imgMarkdowns, "\n")
		}
		message.Content = content
	}

	message.ReasoningContent = response.ReasoningText
	message.Canvases = providers.CanvasFiles(response.Canvases)

	// Logic: Construct Response
	return &dto.ChatCompletionResponse{
		ID:             fmt.Sprintf("chatcmpl-%d", time.Now().Unix()),
		Object:         "chat.completion",
		Created:        time.Now().Unix(),
		Model:          response.Model,
		RequestedModel: response.RequestedModel,
		Choices: []dto.Choice{
			{
				Index:        0,
				Message:      message,
				FinishReason: finishReason,
			},
		},
		Usage: models.Usage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}, nil
}

func (s *OpenAIService) CreateImageGeneration(ctx context.Context, req dto.ImageGenerationRequest) (*dto.ImageGenerationResponse, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	n := req.N
	if n <= 0 {
		n = 1
	}
	if n > 10 {
		return nil, fmt.Errorf("n must be between 1 and 10")
	}

	imagePrompt := buildImageGenerationPrompt(prompt, req.Size)
	wantB64 := strings.EqualFold(req.ResponseFormat, "b64_json")
	opts := []providers.GenerateOption{}
	if req.Model != "" {
		opts = append(opts, providers.WithModel(req.Model))
	}
	if wantB64 {
		opts = append(opts, providers.WithGeneratedImageDownload(true))
	}

	data := make([]dto.ImageGenerationData, 0, n)
	for len(data) < n {
		response, err := s.client.GenerateContent(ctx, imagePrompt, opts...)
		if err != nil {
			return nil, err
		}

		generatedImages := make([]providers.Image, 0, len(response.Images))
		for _, image := range response.Images {
			if image.Generated {
				generatedImages = append(generatedImages, image)
			}
		}
		if len(generatedImages) == 0 {
			return nil, fmt.Errorf("provider returned no generated images")
		}

		for _, image := range generatedImages {
			if len(data) >= n {
				break
			}
			item := dto.ImageGenerationData{RevisedPrompt: prompt}
			if wantB64 {
				if image.B64JSON == "" {
					return nil, fmt.Errorf("provider returned image metadata without image bytes")
				}
				item.B64JSON = image.B64JSON
			} else {
				item.URL = image.URL
			}
			data = append(data, item)
		}
	}

	return &dto.ImageGenerationResponse{
		Created: time.Now().Unix(),
		Data:    data,
	}, nil
}

func buildImageGenerationPrompt(prompt, size string) string {
	var b strings.Builder
	b.WriteString("Generate an image from this prompt. Return the generated image, not only a text description.\n\nPrompt: ")
	b.WriteString(prompt)
	if strings.TrimSpace(size) != "" {
		b.WriteString("\nRequested size/aspect: ")
		b.WriteString(strings.TrimSpace(size))
	}
	return b.String()
}

// openAIVideoIDPrefix matches OpenAI video IDs; the job store uses bare IDs.
const openAIVideoIDPrefix = "video_"

// expectedVideoSeconds is how long Gemini Web usually takes, for progress.
const expectedVideoSeconds = 90

// CreateVideo starts a background video job (OpenAI Videos API).
func (s *OpenAIService) CreateVideo(req dto.VideoGenerationRequest) (*dto.Video, error) {
	aspect, err := providers.ParseVideoAspect(req.Size, req.AspectRatio)
	if err != nil {
		return nil, err
	}
	job, err := s.client.StartVideoJob(req.Prompt, req.Model, aspect)
	if err != nil {
		return nil, err
	}
	return toOpenAIVideo(job, time.Now()), nil
}

// GetVideo returns a video job by OpenAI ID.
func (s *OpenAIService) GetVideo(id string) (*dto.Video, error) {
	job, err := s.client.GetVideoJob(strings.TrimPrefix(id, openAIVideoIDPrefix))
	if err != nil {
		return nil, err
	}
	return toOpenAIVideo(job, time.Now()), nil
}

// ListVideos returns retained video jobs, newest first.
func (s *OpenAIService) ListVideos() *dto.VideoList {
	now := time.Now()
	list := &dto.VideoList{Object: "list", Data: []dto.Video{}}
	for _, job := range s.client.ListVideoJobs() {
		list.Data = append(list.Data, *toOpenAIVideo(job, now))
	}
	if n := len(list.Data); n > 0 {
		list.FirstID, list.LastID = &list.Data[0].ID, &list.Data[n-1].ID
	}
	return list
}

// DeleteVideo discards a finished video job.
func (s *OpenAIService) DeleteVideo(id string) (*dto.VideoDeleted, error) {
	if err := s.client.DeleteVideoJob(strings.TrimPrefix(id, openAIVideoIDPrefix)); err != nil {
		return nil, err
	}
	return &dto.VideoDeleted{ID: id, Object: "video.deleted", Deleted: true}, nil
}

// VideoContent returns the MP4 bytes of a completed video job.
func (s *OpenAIService) VideoContent(id string) ([]byte, string, error) {
	job, data, err := s.client.VideoJobContent(strings.TrimPrefix(id, openAIVideoIDPrefix))
	if err != nil {
		return nil, "", err
	}
	mimeType := job.Video.MimeType
	if mimeType == "" {
		mimeType = "video/mp4"
	}
	return data, mimeType, nil
}

func toOpenAIVideo(job providers.VideoJob, now time.Time) *dto.Video {
	id := openAIVideoIDPrefix + job.ID
	video := &dto.Video{
		ID:             id,
		Object:         "video",
		Model:          job.Model,
		Status:         job.Status,
		CreatedAt:      job.CreatedAt.Unix(),
		Prompt:         job.Prompt,
		Seconds:        "8",
		Size:           job.Size(),
		ConversationID: job.ConversationID,
		Message:        job.Message,
	}
	if job.Video.Duration > 0 {
		video.Seconds = strconv.Itoa(int(math.Round(job.Video.Duration)))
	}
	switch job.Status {
	case providers.VideoJobInProgress:
		video.Progress = min(95, int(now.Sub(job.CreatedAt).Seconds()*100/expectedVideoSeconds))
	case providers.VideoJobCompleted:
		video.Progress = 100
		video.ContentURL = "/openai/v1/videos/" + id + "/content"
	}
	if !job.CompletedAt.IsZero() {
		completed, expires := job.CompletedAt.Unix(), job.ExpiresAt().Unix()
		video.CompletedAt, video.ExpiresAt = &completed, &expires
	}
	if job.Error != nil {
		video.Error = &dto.VideoJobError{Code: job.Error.Code, Message: job.Error.Message}
	}
	return video
}

// CreateChatCompletionStream handles OpenAI streaming logic within the service layer.
func (s *OpenAIService) CreateChatCompletionStream(ctx context.Context, req dto.ChatCompletionRequest, onEvent func(dto.ChatCompletionChunk) bool) error {
	response, err := s.CreateChatCompletion(ctx, req)
	if err != nil {
		return err
	}

	chunkID := response.ID
	created := response.Created
	choice := response.Choices[0]

	// Case 1: Tool Calls
	if len(choice.Message.ToolCalls) > 0 {
		for i, tc := range choice.Message.ToolCalls {
			delta := dto.ChatCompletionChunkDelta{}
			if i == 0 {
				delta.Role = "assistant"
			}
			delta.ToolCalls = []dto.ChatCompletionChunkDeltaToolCall{
				{
					Index: i,
					ID:    tc.ID,
					Type:  "function",
					Function: dto.ChatCompletionChunkDeltaToolFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				},
			}

			if !onEvent(dto.ChatCompletionChunk{
				ID:      chunkID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   response.Model,
				Choices: []dto.ChunkChoice{{Index: 0, Delta: delta}},
			}) {
				return nil
			}
		}

		// Final tool_calls chunk
		onEvent(dto.ChatCompletionChunk{
			ID:      chunkID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   response.Model,
			Choices: []dto.ChunkChoice{{Index: 0, FinishReason: "tool_calls"}},
		})
		return nil
	}

	// Case 2: Regular Text
	firstChunk := true
	if choice.Message.ReasoningContent != "" {
		reasoningChunks := utils.SplitResponseIntoChunks(choice.Message.ReasoningContent, 30)
		for _, content := range reasoningChunks {
			delta := dto.ChatCompletionChunkDelta{ReasoningContent: content}
			if firstChunk {
				delta.Role = "assistant"
				firstChunk = false
			}
			if !onEvent(dto.ChatCompletionChunk{
				ID:      chunkID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   response.Model,
				Choices: []dto.ChunkChoice{{
					Index: 0,
					Delta: delta,
				}},
			}) {
				return nil
			}
			if !utils.SleepWithCancel(ctx, 30*time.Millisecond) {
				return nil
			}
		}
	}

	chunks := utils.SplitResponseIntoChunks(choice.Message.Content, 30)
	for _, content := range chunks {
		delta := dto.ChatCompletionChunkDelta{Content: content}
		if firstChunk {
			delta.Role = "assistant"
			firstChunk = false
		}
		if !onEvent(dto.ChatCompletionChunk{
			ID:      chunkID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   response.Model,
			Choices: []dto.ChunkChoice{{Index: 0, Delta: delta}},
		}) {
			return nil
		}
		if !utils.SleepWithCancel(ctx, 30*time.Millisecond) {
			return nil
		}
	}

	// Final text chunk; canvases are sent whole, once.
	onEvent(dto.ChatCompletionChunk{
		ID:      chunkID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   response.Model,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: dto.ChatCompletionChunkDelta{Canvases: choice.Message.Canvases}, FinishReason: choice.FinishReason}},
	})

	return nil
}

type toolBridgePayload struct {
	ToolCalls []toolBridgeCall `json:"tool_calls"`
	Content   string           `json:"content"`
}

type toolBridgeCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *OpenAIService) buildToolBridgePrompt(req dto.ChatCompletionRequest, basePrompt string) string {
	var b strings.Builder
	b.WriteString("You are an OpenAI-compatible assistant running behind a bridge to Gemini web.\n")
	b.WriteString("You MUST respond with JSON only. Do not output markdown code fences.\n")
	b.WriteString("Output schema:\n")
	b.WriteString("{\"tool_calls\":[{\"name\":\"<tool_name>\",\"arguments\":{}}]} OR {\"content\":\"<assistant_text>\"}\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Use only tool names listed below.\n")
	b.WriteString("- arguments must be valid JSON object.\n")

	toolChoiceMode := req.ToolChoiceMode()
	if toolChoiceMode == "required" {
		b.WriteString("- You must return at least one tool call.\n")
	}
	if toolChoiceMode == "function" {
		forced := req.ForcedToolName()
		if forced != "" {
			b.WriteString("- You must return exactly one tool call with name: ")
			b.WriteString(forced)
			b.WriteString("\n")
		}
	}
	if toolChoiceMode == "none" {
		b.WriteString("- Tool calling disabled. Return only {\"content\":\"...\"}.\n")
	}

	b.WriteString("Available tools:\n")
	for _, t := range req.Tools {
		if !strings.EqualFold(t.Type, "function") || strings.TrimSpace(t.Function.Name) == "" {
			continue
		}
		b.WriteString("- name: ")
		b.WriteString(strings.TrimSpace(t.Function.Name))
		if strings.TrimSpace(t.Function.Description) != "" {
			b.WriteString(" | description: ")
			b.WriteString(strings.TrimSpace(t.Function.Description))
		}
		if len(t.Function.Parameters) > 0 {
			b.WriteString(" | parameters: ")
			b.Write(t.Function.Parameters)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nConversation:\n")
	b.WriteString(basePrompt)
	return b.String()
}

func (s *OpenAIService) parseToolBridgeOutput(req dto.ChatCompletionRequest, text string) ([]dto.ChatCompletionToolCall, string) {
	cleaned := utils.StripCodeFence(text)
	if cleaned == "" {
		return nil, ""
	}

	payload, ok := decodeToolBridgePayload(cleaned)
	if !ok {
		return nil, strings.TrimSpace(text)
	}

	allowed := make(map[string]struct{}, len(req.Tools))
	for _, t := range req.Tools {
		if strings.EqualFold(t.Type, "function") {
			name := strings.TrimSpace(t.Function.Name)
			if name != "" {
				allowed[name] = struct{}{}
			}
		}
	}

	forcedName := req.ForcedToolName()
	calls := make([]dto.ChatCompletionToolCall, 0, len(payload.ToolCalls))
	for i, tc := range payload.ToolCalls {
		name := strings.TrimSpace(tc.Name)
		if name == "" {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		if forcedName != "" && name != forcedName {
			continue
		}

		calls = append(calls, dto.ChatCompletionToolCall{
			ID:   fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i),
			Type: "function",
			Function: dto.ChatCompletionToolCallFunction{
				Name:      name,
				Arguments: normalizeArguments(tc.Arguments),
			},
		})
	}

	content := strings.TrimSpace(payload.Content)
	if content == "" && len(calls) == 0 {
		content = strings.TrimSpace(text)
	}
	return calls, content
}

func (s *OpenAIService) buildFallbackToolCalls(req dto.ChatCompletionRequest) []dto.ChatCompletionToolCall {
	forced := req.ForcedToolName()
	if forced != "" {
		return []dto.ChatCompletionToolCall{
			{
				ID:   fmt.Sprintf("call_%d_0", time.Now().UnixNano()),
				Type: "function",
				Function: dto.ChatCompletionToolCallFunction{
					Name:      forced,
					Arguments: "{}",
				},
			},
		}
	}

	if req.ToolChoiceMode() == "required" {
		for _, t := range req.Tools {
			if strings.EqualFold(t.Type, "function") && strings.TrimSpace(t.Function.Name) != "" {
				return []dto.ChatCompletionToolCall{
					{
						ID:   fmt.Sprintf("call_%d_0", time.Now().UnixNano()),
						Type: "function",
						Function: dto.ChatCompletionToolCallFunction{
							Name:      strings.TrimSpace(t.Function.Name),
							Arguments: "{}",
						},
					},
				}
			}
		}
	}

	return nil
}

func decodeToolBridgePayload(text string) (toolBridgePayload, bool) {
	var payload toolBridgePayload
	if err := json.Unmarshal([]byte(text), &payload); err == nil {
		return payload, true
	}

	obj := extractFirstJSONObject(text)
	if obj == "" {
		return toolBridgePayload{}, false
	}
	if err := json.Unmarshal([]byte(obj), &payload); err != nil {
		return toolBridgePayload{}, false
	}
	return payload, true
}

func extractFirstJSONObject(text string) string {
	return utils.ExtractFirstJSONObject(text)
}

func normalizeArguments(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}

	if strings.HasPrefix(trimmed, "\"") {
		var asString string
		if err := json.Unmarshal(raw, &asString); err == nil {
			trimmed = strings.TrimSpace(asString)
			if trimmed == "" {
				return "{}"
			}
		}
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(trimmed)); err != nil {
		return "{}"
	}
	return compact.String()
}
