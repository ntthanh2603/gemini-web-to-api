package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// maxToolBridgeAttempts bounds how many times Gemini is asked for a tool-bridge
// reply before the request fails with ErrInvalidToolCall.
const maxToolBridgeAttempts = 3

// ErrInvalidToolCall reports that Gemini never produced a usable tool call.
// Tool calls are never fabricated, so callers get an error to retry on instead
// of a call with missing arguments.
var ErrInvalidToolCall = errors.New("gemini did not return a valid tool call")

type OpenAIService struct {
	client *providers.Client
	log    *zap.Logger
	// generate sends a prompt to Gemini; tests replace it.
	generate func(ctx context.Context, prompt string, options ...providers.GenerateOption) (*providers.Response, error)
}

func NewOpenAIService(client *providers.Client, log *zap.Logger) *OpenAIService {
	return &OpenAIService{
		client:   client,
		log:      log,
		generate: client.GenerateContent,
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
	if _, err := providers.ParseThinkingLevel(geminiThinkingLevel(req.ReasoningEffort)); err != nil {
		return fmt.Errorf("invalid reasoning_effort: %w", err)
	}
	_, err := s.client.ResolveModel(req.Model)
	return err
}

// geminiThinkingLevel clamps OpenAI reasoning_effort values that Gemini Web
// has no level for onto the nearest one, so clients that send them by default
// keep working instead of getting a 400.
func geminiThinkingLevel(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal":
		return "low"
	case "xhigh":
		return "high"
	}
	return effort
}

// cleanGeneratedText strips Gemini's image placeholders from a reply so the
// markdown links added afterwards are the only image references.
func cleanGeneratedText(text string) string {
	return strings.TrimSpace(imagePlaceholderRegex.ReplaceAllString(text, ""))
}

// withImageMarkdown appends generated images to a text reply as markdown links.
func withImageMarkdown(content string, images []providers.Image) string {
	markdowns := make([]string, 0, len(images))
	for _, img := range images {
		markdowns = append(markdowns, fmt.Sprintf("![image](%s)", img.URL))
	}
	if content == "" {
		return strings.Join(markdowns, "\n")
	}
	return content + "\n\n" + strings.Join(markdowns, "\n")
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

	// response_format is best-effort: ask for the shape in the prompt. With
	// tools enabled the tool bridge prompt carries the rule instead, because it
	// already owns the reply format.
	jsonSchema, jsonMode := req.JSONResponseRequested()
	if jsonMode && !req.HasToolsEnabled() {
		prompt = buildJSONModePrompt(prompt, jsonSchema)
	}

	if req.HasToolsEnabled() {
		prompt = s.buildToolBridgePrompt(req, prompt)
	}

	opts := []providers.GenerateOption{providers.WithThinkingLevel(geminiThinkingLevel(req.ReasoningEffort))}
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
	var response *providers.Response
	message := dto.ChatCompletionResponseMessage{Role: "assistant"}
	finishReason := "stop"

	// regenerate asks Gemini for the reply again; a response_format repair goes
	// through the same path (tool bridge or plain) as the first request.
	var regenerate func(string) (string, error)

	if req.HasToolsEnabled() {
		var toolCalls []dto.ChatCompletionToolCall
		var content string
		response, toolCalls, content, err = s.generateToolBridge(ctx, req, prompt, opts)
		if err != nil {
			return nil, err
		}

		if len(toolCalls) > 0 {
			message.ToolCalls = toolCalls
			finishReason = "tool_calls"
		} else {
			message.Content = content
		}
		// One request, not generateToolBridge and its retries, so the repair
		// stays a single extra request; a tool call in it is not used.
		regenerate = func(repairPrompt string) (string, error) {
			repaired, err := s.generate(ctx, repairPrompt, opts...)
			if err != nil {
				return "", err
			}
			_, content, err := s.parseToolBridgeOutput(req, repaired.Text)
			return content, err
		}
	} else {
		response, err = s.generate(ctx, prompt, opts...)
		if err != nil {
			return nil, err
		}
		message.Content = cleanGeneratedText(response.Text)
		regenerate = func(repairPrompt string) (string, error) {
			repaired, err := s.generate(ctx, repairPrompt, opts...)
			if err != nil {
				return "", err
			}
			return cleanGeneratedText(repaired.Text), nil
		}
	}

	// JSON mode applies to a text reply only: when the model called a tool, the
	// tool bridge has already shaped the reply the client asked for.
	if jsonMode && len(message.ToolCalls) == 0 && message.Content != "" {
		message.Content = s.ensureJSONReply(req, prompt, message.Content, jsonSchema, regenerate)
	}

	// Markdown links after the JSON would break the shape a response_format
	// client asked for, so images only ride along with JSON mode when there is
	// no JSON text to corrupt.
	if !req.HasToolsEnabled() && len(response.Images) > 0 && (!jsonMode || message.Content == "") {
		message.Content = withImageMarkdown(message.Content, response.Images)
	}

	message.ReasoningContent = response.ReasoningText
	message.Canvases = providers.CanvasFiles(response.Canvases)

	// Logic: Construct Response
	tokens := response.CountTokens(prompt)
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
			PromptTokens:     tokens.PromptTokens,
			CompletionTokens: tokens.CompletionTokens,
			TotalTokens:      tokens.TotalTokens,
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

// CreateSpeech generates music with Gemini Web for the audio/speech endpoint
// and returns the audio bytes with their media type.
func (s *OpenAIService) CreateSpeech(ctx context.Context, req dto.SpeechRequest) ([]byte, string, error) {
	prompt := strings.TrimSpace(req.Input)
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" && prompt != "" {
		prompt += "\n\nStyle: " + instructions
	}
	config := providers.MusicConfig{Length: req.Length, Genre: req.Genre}
	switch strings.ToLower(req.VoiceName()) {
	case providers.MusicInstrumental, providers.MusicVocalsOn:
		config.Vocals = strings.ToLower(req.VoiceName())
	}
	// OpenAI speech model names (tts-1, gpt-4o-mini-tts, ...) use the default model.
	model := req.Model
	if lower := strings.ToLower(model); strings.HasPrefix(lower, "tts") || strings.Contains(lower, "-tts") {
		model = ""
	}
	result, err := s.client.GenerateMusic(ctx, prompt, model, config)
	if err != nil {
		return nil, "", err
	}
	mimeType := result.Audio.MimeType
	if mimeType == "" {
		mimeType = "audio/mpeg"
	}
	return result.Data, mimeType, nil
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

	// finish sends the final chunk. With stream_options.include_usage, OpenAI
	// then ends the stream with a chunk that has no choices and the usage.
	finish := func(final dto.ChatCompletionChunk) {
		if onEvent(final) && req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
			onEvent(dto.ChatCompletionChunk{
				ID:      chunkID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   response.Model,
				Choices: []dto.ChunkChoice{},
				Usage:   &response.Usage,
			})
		}
	}

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
		finish(dto.ChatCompletionChunk{
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
	finish(dto.ChatCompletionChunk{
		ID:      chunkID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   response.Model,
		Choices: []dto.ChunkChoice{{Index: 0, Delta: dto.ChatCompletionChunkDelta{Canvases: choice.Message.Canvases}, FinishReason: choice.FinishReason}},
	})

	return nil
}

type toolBridgePayload struct {
	ToolCalls []toolBridgeCall  `json:"tool_calls"`
	Content   toolBridgeContent `json:"content"`
}

// toolBridgeContent is the bridge reply's "content". It is normally a string,
// but under response_format the model may put the JSON reply itself there as an
// object or array, which is kept as its JSON text.
type toolBridgeContent string

func (c *toolBridgeContent) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*c = toolBridgeContent(text)
		return nil
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return errors.New("content must be a string, object or array")
	}
	*c = toolBridgeContent(trimmed)
	return nil
}

type toolBridgeCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	// Keys Gemini sometimes uses instead of "arguments", and the nested OpenAI
	// shape {"type":"function","function":{"name":...,"arguments":...}}.
	Args       json.RawMessage `json:"args"`
	Parameters json.RawMessage `json:"parameters"`
	Input      json.RawMessage `json:"input"`
	Function   *toolBridgeCall `json:"function"`
}

func (c toolBridgeCall) toolName() string {
	if name := strings.TrimSpace(c.Name); name != "" || c.Function == nil {
		return name
	}
	return c.Function.toolName()
}

func (c toolBridgeCall) rawArguments() json.RawMessage {
	for _, raw := range []json.RawMessage{c.Arguments, c.Args, c.Parameters, c.Input} {
		if len(raw) > 0 {
			return raw
		}
	}
	if c.Function != nil {
		return c.Function.rawArguments()
	}
	return nil
}

// generateToolBridge asks Gemini for a tool-bridge reply and retries, with a
// note about what was wrong, while the reply has no usable tool call. It never
// fabricates a call: when every attempt fails it returns ErrInvalidToolCall.
func (s *OpenAIService) generateToolBridge(ctx context.Context, req dto.ChatCompletionRequest, prompt string, opts []providers.GenerateOption) (*providers.Response, []dto.ChatCompletionToolCall, string, error) {
	mustCall := req.ToolChoiceMode() == "required" || req.ToolChoiceMode() == "function"
	attemptPrompt := prompt
	var lastErr error
	for attempt := 1; attempt <= maxToolBridgeAttempts; attempt++ {
		response, err := s.generate(ctx, attemptPrompt, opts...)
		if err != nil {
			return nil, nil, "", err
		}

		calls, content, err := s.parseToolBridgeOutput(req, response.Text)
		if err == nil && mustCall && len(calls) == 0 {
			err = errors.New("the reply contained no tool call")
		}
		if err == nil {
			return response, calls, content, nil
		}

		lastErr = err
		s.log.Warn("Gemini returned an unusable tool call",
			zap.Int("attempt", attempt), zap.Int("max_attempts", maxToolBridgeAttempts),
			zap.String("model", req.Model), zap.Error(err))
		s.log.Debug("Unusable tool bridge reply", zap.String("text", response.Text))
		attemptPrompt = prompt + "\n\nYour previous reply could not be used: " + err.Error() +
			". Reply again with JSON only, exactly matching the output schema above, and set every required argument."
	}
	return nil, nil, "", fmt.Errorf("%w after %d attempts: %v", ErrInvalidToolCall, maxToolBridgeAttempts, lastErr)
}

func (s *OpenAIService) buildToolBridgePrompt(req dto.ChatCompletionRequest, basePrompt string) string {
	var b strings.Builder
	b.WriteString("You are an OpenAI-compatible assistant running behind a bridge to Gemini web.\n")
	b.WriteString("You MUST respond with JSON only. Do not output markdown code fences.\n")
	b.WriteString("Output schema:\n")
	b.WriteString("{\"tool_calls\":[{\"name\":\"<tool_name>\",\"arguments\":{\"<parameter>\":<value>}}]} OR ")
	if _, jsonMode := req.JSONResponseRequested(); jsonMode {
		// The reply itself is JSON: show it as a value, not a string, or the
		// model follows this template and writes prose.
		b.WriteString("{\"content\":<reply_json>}\n")
	} else {
		b.WriteString("{\"content\":\"<assistant_text>\"}\n")
	}
	b.WriteString("Rules:\n")
	b.WriteString("- Use only tool names listed below.\n")
	b.WriteString("- arguments must be a JSON object that sets every required parameter of the tool's parameters schema.\n")
	b.WriteString("- Escape newlines and double quotes inside JSON strings as \\n and \\\".\n")

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
	if schema, jsonMode := req.JSONResponseRequested(); jsonMode {
		if len(schema) > 0 {
			b.WriteString("- Without a tool call, \"content\" must be the reply itself as a JSON value (not a string) matching this JSON Schema: ")
			b.Write(schema)
			b.WriteString(". Set every required property.\n")
		} else {
			b.WriteString("- Without a tool call, \"content\" must be the reply itself as a JSON object (not a string), with the fields the conversation asks for.\n")
		}
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

// parseToolBridgeOutput extracts tool calls from a tool-bridge reply. A reply
// that is not bridge JSON is returned as plain assistant text. An error means
// the model tried to call a tool but the call cannot be used (unknown tool,
// malformed or incomplete arguments), so it must not reach the client.
func (s *OpenAIService) parseToolBridgeOutput(req dto.ChatCompletionRequest, text string) ([]dto.ChatCompletionToolCall, string, error) {
	cleaned := utils.StripCodeFence(text)
	if cleaned == "" {
		return nil, "", nil
	}

	payload, ok := decodeToolBridgePayload(cleaned)
	if !ok {
		return nil, strings.TrimSpace(text), nil
	}

	schemas := make(map[string]json.RawMessage, len(req.Tools))
	for _, t := range req.Tools {
		if strings.EqualFold(t.Type, "function") {
			name := strings.TrimSpace(t.Function.Name)
			if name != "" {
				schemas[name] = t.Function.Parameters
			}
		}
	}

	forcedName := req.ForcedToolName()
	calls := make([]dto.ChatCompletionToolCall, 0, len(payload.ToolCalls))
	for i, tc := range payload.ToolCalls {
		name := tc.toolName()
		if name == "" {
			continue
		}
		schema, known := schemas[name]
		if len(schemas) > 0 && !known {
			continue
		}
		if forcedName != "" && name != forcedName {
			continue
		}

		args, err := normalizeArguments(tc.rawArguments())
		if err != nil {
			return nil, "", fmt.Errorf("tool %q: %w", name, err)
		}
		if err := checkRequiredArguments(schema, args); err != nil {
			return nil, "", fmt.Errorf("tool %q: %w", name, err)
		}

		calls = append(calls, dto.ChatCompletionToolCall{
			ID:   fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i),
			Type: "function",
			Function: dto.ChatCompletionToolCallFunction{
				Name:      name,
				Arguments: args,
			},
		})
	}
	if len(payload.ToolCalls) > 0 && len(calls) == 0 {
		return nil, "", errors.New("no tool call named one of the requested tools")
	}

	content := strings.TrimSpace(string(payload.Content))
	if content == "" && len(calls) == 0 {
		content = strings.TrimSpace(text)
	}
	return calls, content, nil
}

// decodeToolBridgePayload finds the bridge JSON in a reply. It tolerates prose
// around the JSON (including stray braces) and raw newlines inside JSON
// strings, which Gemini often writes for multi-line argument values.
func decodeToolBridgePayload(text string) (toolBridgePayload, bool) {
	if payload, ok := unmarshalToolBridgePayload(text); ok {
		return payload, true
	}

	for start := strings.Index(text, "{"); start >= 0; {
		if obj := utils.ExtractFirstJSONObject(text[start:]); obj != "" {
			if payload, ok := unmarshalToolBridgePayload(obj); ok {
				return payload, true
			}
		}
		next := strings.Index(text[start+1:], "{")
		if next < 0 {
			break
		}
		start += next + 1
	}
	return toolBridgePayload{}, false
}

// unmarshalToolBridgePayload decodes text as bridge JSON. Objects without
// tool_calls or content (such as a nested argument object) are rejected.
func unmarshalToolBridgePayload(text string) (toolBridgePayload, bool) {
	var payload toolBridgePayload
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		payload = toolBridgePayload{}
		if err := json.Unmarshal([]byte(utils.EscapeJSONStringControlChars(text)), &payload); err != nil {
			return toolBridgePayload{}, false
		}
	}
	if len(payload.ToolCalls) == 0 && strings.TrimSpace(string(payload.Content)) == "" {
		return toolBridgePayload{}, false
	}
	return payload, true
}

// normalizeArguments returns tool-call arguments as a compact JSON object.
// Missing arguments mean an empty object; anything that is not a JSON object
// is an error instead of being replaced with "{}".
func normalizeArguments(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "{}", nil
	}

	// Some replies encode the arguments as a JSON string.
	if strings.HasPrefix(trimmed, "\"") {
		var asString string
		if err := json.Unmarshal([]byte(trimmed), &asString); err != nil {
			return "", fmt.Errorf("arguments are not valid JSON: %w", err)
		}
		trimmed = strings.TrimSpace(asString)
		if trimmed == "" {
			return "{}", nil
		}
	}

	if !strings.HasPrefix(trimmed, "{") {
		return "", errors.New("arguments must be a JSON object")
	}
	if !json.Valid([]byte(trimmed)) {
		trimmed = utils.EscapeJSONStringControlChars(trimmed)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(trimmed)); err != nil {
		return "", fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	return compact.String(), nil
}

// toolSchema is the part of a JSON Schema needed to find missing arguments.
type toolSchema struct {
	Properties map[string]toolSchema `json:"properties"`
	Required   []string              `json:"required"`
}

// checkRequiredArguments reports the first required property that args
// leaves out, including those of nested objects. It is not a full JSON Schema
// validator: schemas it cannot read are not checked.
func checkRequiredArguments(schema json.RawMessage, args string) error {
	if len(schema) == 0 {
		return nil
	}
	var parsed toolSchema
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return nil
	}
	return missingRequired(parsed, json.RawMessage(args), "")
}

func missingRequired(schema toolSchema, value json.RawMessage, path string) error {
	if len(schema.Required) == 0 && len(schema.Properties) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(value, &obj); err != nil {
		return nil
	}
	for _, key := range schema.Required {
		if _, ok := obj[key]; !ok {
			return fmt.Errorf("missing required argument %q", path+key)
		}
	}
	for key, sub := range schema.Properties {
		if v, ok := obj[key]; ok {
			if err := missingRequired(sub, v, path+key+"."); err != nil {
				return err
			}
		}
	}
	return nil
}
