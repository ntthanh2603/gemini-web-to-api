package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gemini-web-to-api/internal/commons/utils"
	"gemini-web-to-api/internal/modules/gemini/dto"
	"gemini-web-to-api/internal/modules/providers"

	"go.uber.org/zap"
)

type GeminiService struct {
	client *providers.Client
	log    *zap.Logger
}

func NewGeminiService(client *providers.Client, log *zap.Logger) *GeminiService {
	return &GeminiService{
		client: client,
		log:    log,
	}
}

func (s *GeminiService) ListModels() []providers.ModelInfo {
	return s.client.ListModels()
}

func (s *GeminiService) ResolveModel(model string) (providers.ModelInfo, error) {
	return s.client.ResolveModel(model)
}

func (s *GeminiService) GenerateContent(ctx context.Context, modelID string, req dto.GeminiGenerateRequest) (*dto.GeminiGenerateResponse, error) {
	prompt, inputFiles, err := s.buildPrompt(req)
	if err != nil {
		return nil, err
	}

	// Logic: Call Provider
	opts := []providers.GenerateOption{providers.WithModel(modelID)}
	if len(inputFiles) > 0 {
		opts = append(opts, providers.WithInputFiles(inputFiles))
	}
	response, err := s.client.GenerateContent(ctx, prompt, opts...)
	if err != nil {
		return nil, err
	}

	// Logic: Construct Response
	resParts := []dto.Part{}
	finishReason := "STOP"

	if response.ReasoningText != "" {
		resParts = append(resParts, dto.Part{Text: response.ReasoningText, Thought: true})
	}

	if len(req.Tools) > 0 {
		functionCalls, content := s.parseToolBridgeOutput(req, response.Text)
		if len(functionCalls) > 0 {
			for _, fc := range functionCalls {
				resParts = append(resParts, dto.Part{FunctionCall: &fc})
			}
			finishReason = "FUNCTION_CALL"
		} else {
			resParts = append(resParts, dto.Part{Text: content})
		}
	} else {
		if response.Text != "" {
			resParts = append(resParts, dto.Part{Text: response.Text})
		}
		for _, image := range response.Images {
			resParts = append(resParts, dto.Part{
				FileData: &dto.FileData{
					MimeType: image.MimeType,
					FileURI:  image.URL,
				},
			})
		}
		// Canvas files ("-canvas" models) are returned as inline file parts.
		for _, canvas := range providers.CanvasFiles(response.Canvases) {
			resParts = append(resParts, dto.Part{
				InlineData: &dto.InlineData{
					MimeType:    canvas.MimeType,
					Data:        base64.StdEncoding.EncodeToString([]byte(canvas.Content)),
					DisplayName: canvas.FileName,
				},
			})
		}
		if len(resParts) == 0 {
			resParts = append(resParts, dto.Part{Text: ""})
		}
	}

	return &dto.GeminiGenerateResponse{
		Candidates: []dto.Candidate{
			{
				Index: 0,
				Content: dto.Content{
					Role:  "model",
					Parts: resParts,
				},
				FinishReason: finishReason,
			},
		},
		UsageMetadata: &dto.UsageMetadata{
			TotalTokenCount: 0,
		},
	}, nil
}

func (s *GeminiService) buildPrompt(req dto.GeminiGenerateRequest) (string, []providers.InputFile, error) {
	var promptBuilder strings.Builder

	// Support system_instruction
	if req.SystemInstruction != nil {
		var sysText strings.Builder
		for _, part := range req.SystemInstruction.Parts {
			if part.Text != "" {
				sysText.WriteString(part.Text)
				sysText.WriteString(" ")
			}
		}
		if trimmedSys := strings.TrimSpace(sysText.String()); trimmedSys != "" {
			promptBuilder.WriteString(fmt.Sprintf("System: %s\n\n", trimmedSys))
		}
	}

	inputFiles := make([]providers.InputFile, 0)
	isMultiTurn := len(req.Contents) > 1
	for _, content := range req.Contents {
		rolePrefix := ""
		if isMultiTurn {
			switch strings.ToLower(strings.TrimSpace(content.Role)) {
			case "model", "assistant":
				rolePrefix = "Model: "
			case "system":
				rolePrefix = "System: "
			default:
				rolePrefix = "User: "
			}
		}

		for i, part := range content.Parts {
			if part.Text != "" {
				if rolePrefix != "" {
					promptBuilder.WriteString(rolePrefix)
					rolePrefix = ""
				}
				promptBuilder.WriteString(part.Text)
				promptBuilder.WriteString("\n")
			}
			if part.InlineData != nil && part.InlineData.Data != "" {
				data, err := providers.DecodeBase64Data(part.InlineData.Data)
				if err != nil {
					return "", nil, fmt.Errorf("decode inline_data: %w", err)
				}
				inputFiles = append(inputFiles, providers.InputFile{
					Name:     fmt.Sprintf("inline_%d%s", i+1, extensionForMimeType(part.InlineData.MimeType)),
					MimeType: part.InlineData.MimeType,
					Data:     data,
				})
			}
		}
	}

	prompt := strings.TrimSpace(promptBuilder.String())
	if prompt == "" && len(inputFiles) == 0 {
		return "", nil, fmt.Errorf("empty content")
	}
	if prompt == "" {
		prompt = fmt.Sprintf("[%d file(s) attached]", len(inputFiles))
	}

	if len(req.Tools) > 0 {
		prompt = s.buildToolBridgePrompt(req, prompt)
	}

	return prompt, inputFiles, nil
}

func extensionForMimeType(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}

// GenerateContentStream handles Gemini's streaming simulation logic in the service layer.
func (s *GeminiService) GenerateContentStream(ctx context.Context, modelID string, req dto.GeminiGenerateRequest, onEvent func(dto.GeminiGenerateResponse) bool) error {
	resp, err := s.GenerateContent(ctx, modelID, req)
	if err != nil {
		return err
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil
	}

	candidate := resp.Candidates[0]
	hasFunctionCall := false
	for _, part := range candidate.Content.Parts {
		if part.FunctionCall != nil {
			hasFunctionCall = true
			break
		}
	}

	if hasFunctionCall {
		// Send as one chunk if it's a function call
		onEvent(*resp)
		return nil
	}

	// Simulated text streaming part-by-part
	for _, part := range candidate.Content.Parts {
		if part.Text == "" {
			continue
		}
		chunks := utils.SplitResponseIntoChunks(part.Text, 30)
		for _, content := range chunks {
			if !onEvent(dto.GeminiGenerateResponse{
				Candidates: []dto.Candidate{
					{
						Index: 0,
						Content: dto.Content{
							Role:  "model",
							Parts: []dto.Part{{Text: content, Thought: part.Thought}},
						},
					},
				},
			}) {
				return nil
			}
			if !utils.SleepWithCancel(ctx, 30*time.Millisecond) {
				return nil
			}
		}
	}

	// Final STOP chunk carries the file parts (canvases, images) whole.
	var fileParts []dto.Part
	for _, part := range candidate.Content.Parts {
		if part.InlineData != nil || part.FileData != nil {
			fileParts = append(fileParts, part)
		}
	}
	final := dto.Candidate{Index: 0, FinishReason: "STOP"}
	if len(fileParts) > 0 {
		final.Content = dto.Content{Role: "model", Parts: fileParts}
	}
	onEvent(dto.GeminiGenerateResponse{Candidates: []dto.Candidate{final}})

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

func (s *GeminiService) buildToolBridgePrompt(req dto.GeminiGenerateRequest, basePrompt string) string {
	var b strings.Builder
	b.WriteString("You are a Gemini assistant running behind a bridge that supports function calling.\n")
	b.WriteString("You MUST respond with JSON only. Do not output markdown code fences.\n")
	b.WriteString("Output schema:\n")
	b.WriteString("{\"status\":\"call\",\"tool_calls\":[{\"name\":\"<tool_name>\",\"arguments\":{}}]} OR {\"status\":\"text\",\"content\":\"<assistant_text>\"}\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Use only tool names listed below.\n")
	b.WriteString("- arguments must be valid JSON object.\n")

	b.WriteString("Available tools:\n")
	for _, tool := range req.Tools {
		for _, fn := range tool.FunctionDeclarations {
			b.WriteString("- name: ")
			b.WriteString(fn.Name)
			if fn.Description != "" {
				b.WriteString(" | description: ")
				b.WriteString(fn.Description)
			}
			if len(fn.Parameters) > 0 {
				b.WriteString(" | parameters: ")
				b.Write(fn.Parameters)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\nConversation:\n")
	b.WriteString(basePrompt)
	return b.String()
}

func (s *GeminiService) parseToolBridgeOutput(req dto.GeminiGenerateRequest, text string) ([]dto.FunctionCall, string) {
	cleaned := utils.StripCodeFence(text)
	if cleaned == "" {
		return nil, ""
	}

	var payload struct {
		Status    string           `json:"status"`
		ToolCalls []toolBridgeCall `json:"tool_calls"`
		Content   string           `json:"content"`
	}

	if err := json.Unmarshal([]byte(cleaned), &payload); err != nil {
		obj := utils.ExtractFirstJSONObject(text)
		if obj == "" || json.Unmarshal([]byte(obj), &payload) != nil {
			return nil, text
		}
	}

	if payload.Status == "call" && len(payload.ToolCalls) > 0 {
		calls := make([]dto.FunctionCall, 0, len(payload.ToolCalls))
		for _, tc := range payload.ToolCalls {
			calls = append(calls, dto.FunctionCall{
				Name: tc.Name,
				Args: tc.Arguments,
			})
		}
		return calls, ""
	}

	return nil, payload.Content
}

func (s *GeminiService) IsHealthy() bool {
	return s.client.IsHealthy()
}

func (s *GeminiService) Client() *providers.Client {
	return s.client
}

// StartVideoOperation starts a video job for models/{model}:predictLongRunning.
func (s *GeminiService) StartVideoOperation(model string, req dto.PredictLongRunningRequest) (*dto.VideoOperation, error) {
	if len(req.Instances) != 1 {
		return nil, &providers.VideoValidationError{Message: "exactly one instance is required"}
	}
	instance := req.Instances[0]
	if len(instance.Image) > 0 || len(instance.Video) > 0 {
		return nil, &providers.VideoValidationError{Message: "image and video inputs are not supported; only text-to-video is available"}
	}
	aspectRatio := ""
	if req.Parameters != nil {
		if req.Parameters.SampleCount > 1 {
			return nil, &providers.VideoValidationError{Message: "only one video per request is supported (sampleCount must be 1)"}
		}
		aspectRatio = req.Parameters.AspectRatio
	}
	aspect, err := providers.ParseVideoAspect("", aspectRatio)
	if err != nil {
		return nil, err
	}
	job, err := s.client.StartVideoJob(instance.Prompt, model, aspect)
	if err != nil {
		return nil, err
	}
	return &dto.VideoOperation{Name: videoOperationName(model, job.ID)}, nil
}

// GetVideoOperation reports a video job as a long-running operation. fileBaseURL
// is the public "/gemini/v1beta/files" URL the generated video is served from.
func (s *GeminiService) GetVideoOperation(model, id, fileBaseURL string) (*dto.VideoOperation, error) {
	job, err := s.client.GetVideoJob(id)
	if err != nil {
		return nil, err
	}
	operation := &dto.VideoOperation{Name: videoOperationName(model, job.ID), Done: job.Status != providers.VideoJobInProgress}
	switch job.Status {
	case providers.VideoJobCompleted:
		mimeType := job.Video.MimeType
		if mimeType == "" {
			mimeType = "video/mp4"
		}
		operation.Response = &dto.VideoOperationResponse{
			Type: "type.googleapis.com/google.ai.generativelanguage.v1beta.PredictLongRunningResponse",
			GenerateVideoResponse: dto.GenerateVideoResponse{GeneratedSamples: []dto.GeneratedSample{{
				Video: dto.GeneratedVideoFile{URI: fileBaseURL + "/" + job.ID + ":download?alt=media", Encoding: mimeType},
			}}},
		}
	case providers.VideoJobFailed:
		operation.Error = videoOperationError(job.Error)
	}
	if job.ConversationID != "" {
		operation.Metadata = map[string]any{"conversationId": job.ConversationID}
	}
	return operation, nil
}

// VideoFile returns the MP4 bytes of a completed video job.
func (s *GeminiService) VideoFile(id string) ([]byte, string, error) {
	job, data, err := s.client.VideoJobContent(id)
	if err != nil {
		return nil, "", err
	}
	mimeType := job.Video.MimeType
	if mimeType == "" {
		mimeType = "video/mp4"
	}
	return data, mimeType, nil
}

func videoOperationName(model, id string) string {
	return "models/" + strings.TrimPrefix(model, "models/") + "/operations/" + id
}

// videoOperationError maps video failures to google.rpc.Status codes.
func videoOperationError(err *providers.VideoError) *dto.OperationError {
	if err == nil {
		return &dto.OperationError{Code: 13, Message: "video generation failed", Status: "INTERNAL"}
	}
	switch err.Code {
	case providers.VideoErrorQuota:
		return &dto.OperationError{Code: 8, Message: err.Message, Status: "RESOURCE_EXHAUSTED"}
	case providers.VideoErrorRefused:
		return &dto.OperationError{Code: 3, Message: err.Message, Status: "INVALID_ARGUMENT"}
	case providers.VideoErrorTimeout:
		return &dto.OperationError{Code: 4, Message: err.Message, Status: "DEADLINE_EXCEEDED"}
	}
	return &dto.OperationError{Code: 13, Message: err.Message, Status: "INTERNAL"}
}

// DeepResearch performs synchronous deep research
func (s *GeminiService) DeepResearch(ctx context.Context, req dto.DeepResearchRequest) (*dto.DeepResearchResponse, error) {
	opts := []providers.DeepResearchOption{}
	if req.Model != "" {
		opts = append(opts, providers.WithResearchModel(req.Model))
	}
	if req.Language != "" {
		opts = append(opts, providers.WithResearchLanguage(req.Language))
	}
	if req.MaxSources > 0 {
		opts = append(opts, providers.WithResearchMaxSources(req.MaxSources))
	}
	inputFiles, err := providers.InputFilesFromAttachmentList(req.Images)
	if err != nil {
		return nil, err
	}
	if len(inputFiles) > 0 {
		opts = append(opts, providers.WithResearchInputFiles(inputFiles))
	}

	result, err := s.client.DeepResearch(ctx, req.Query, opts...)
	if err != nil {
		return nil, err
	}

	return toDeepResearchResponse(result, "completed"), nil
}

// DeepResearchStream streams deep research events by calling cb for each event
func (s *GeminiService) DeepResearchStream(ctx context.Context, req dto.DeepResearchRequest, cb func(dto.DeepResearchStreamEvent) bool) error {
	opts := []providers.DeepResearchOption{}
	if req.Model != "" {
		opts = append(opts, providers.WithResearchModel(req.Model))
	}
	if req.Language != "" {
		opts = append(opts, providers.WithResearchLanguage(req.Language))
	}
	if req.MaxSources > 0 {
		opts = append(opts, providers.WithResearchMaxSources(req.MaxSources))
	}
	inputFiles, err := providers.InputFilesFromAttachmentList(req.Images)
	if err != nil {
		return err
	}
	if len(inputFiles) > 0 {
		opts = append(opts, providers.WithResearchInputFiles(inputFiles))
	}

	return s.client.DeepResearchStream(ctx, req.Query, func(ev providers.DeepResearchEvent) bool {
		dtoEv := dto.DeepResearchStreamEvent{
			Event:    string(ev.Event),
			Message:  ev.Message,
			Progress: ev.Progress,
			Error:    ev.Error,
		}
		if ev.Step != nil {
			dtoEv.Step = &dto.ResearchStep{
				StepNumber:  ev.Step.StepNumber,
				Type:        ev.Step.Type,
				Description: ev.Step.Description,
				Query:       ev.Step.Query,
				Result:      ev.Step.Result,
			}
		}
		if ev.Source != nil {
			dtoEv.Source = &dto.ResearchSource{
				Title:   ev.Source.Title,
				URL:     ev.Source.URL,
				Snippet: ev.Source.Snippet,
				Domain:  ev.Source.Domain,
			}
		}
		if ev.Result != nil {
			resp := toDeepResearchResponse(ev.Result, "completed")
			dtoEv.Result = resp
		}
		return cb(dtoEv)
	}, opts...)
}

// toDeepResearchResponse converts a providers.DeepResearchResult into a DTO response
func toDeepResearchResponse(r *providers.DeepResearchResult, status string) *dto.DeepResearchResponse {
	resp := &dto.DeepResearchResponse{
		ID:          r.ID,
		Status:      status,
		Query:       r.Query,
		Summary:     r.Summary,
		Model:       r.Model,
		CreatedAt:   r.CreatedAt,
		CompletedAt: r.CompletedAt,
		DurationMs:  r.DurationMs,
	}
	for _, src := range r.Sources {
		resp.Sources = append(resp.Sources, dto.ResearchSource{
			Title:   src.Title,
			URL:     src.URL,
			Snippet: src.Snippet,
			Domain:  src.Domain,
		})
	}
	for _, step := range r.Steps {
		resp.Steps = append(resp.Steps, dto.ResearchStep{
			StepNumber:  step.StepNumber,
			Type:        step.Type,
			Description: step.Description,
			Query:       step.Query,
			Result:      step.Result,
		})
	}
	return resp
}
