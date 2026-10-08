package gemini

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	common "gemini-web-to-api/internal/commons/utils"
	"gemini-web-to-api/internal/modules/gemini/dto"
	"gemini-web-to-api/internal/modules/providers"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

type GeminiController struct {
	service  *GeminiService
	log      *zap.Logger
	mu       sync.RWMutex
	store    *taskStore
	stopChan chan struct{}
}

func NewGeminiController(service *GeminiService, log *zap.Logger) *GeminiController {
	store := newTaskStore()
	stopChan := make(chan struct{})

	// Start background job to purge old tasks periodically
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				store.purgeOlderThan(24 * time.Hour)
			case <-stopChan:
				return
			}
		}
	}()

	return &GeminiController{
		service:  service,
		log:      log,
		store:    store,
		stopChan: stopChan,
	}
}

// Close terminates background tasks
func (h *GeminiController) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopChan != nil {
		close(h.stopChan)
		h.stopChan = nil
	}
}

// SetLogger sets the logger for this handler
func (h *GeminiController) SetLogger(log *zap.Logger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.log = log
}

// IsHealthy returns the health status of the underlying Gemini service
func (h *GeminiController) IsHealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.service == nil {
		return false
	}
	return h.service.IsHealthy()
}

// HandleV1BetaModels returns the list of models in Gemini format
// @Summary List Gemini Models
// @Description Returns a list of models supported by the Gemini API
// @Tags Gemini
// @Accept json
// @Produce json
// @Success 200 {object} dto.GeminiModelsResponse
// @Router /gemini/v1beta/models [get]
func (h *GeminiController) HandleV1BetaModels(c fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	availableModels := h.service.ListModels()
	var geminiModels []dto.GeminiModel
	for _, m := range availableModels {
		geminiModels = append(geminiModels, dto.GeminiModel{
			Name:                       "models/" + m.ID,
			DisplayName:                m.DisplayName,
			SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
		})
	}
	return c.JSON(dto.GeminiModelsResponse{Models: geminiModels})
}

// HandleV1BetaModel returns one dynamically discovered Gemini model.
func (h *GeminiController) HandleV1BetaModel(c fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	model, err := h.service.ResolveModel(c.Params("model"))
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(common.ErrorToResponse(err, "invalid_request_error"))
	}
	return c.JSON(dto.GeminiModel{
		Name:                       "models/" + model.ID,
		DisplayName:                model.DisplayName,
		SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
	})
}

// HandleV1BetaGenerateContent handles the official Gemini generateContent endpoint
// @Summary Generate Content (Gemini)
// @Description Generates content using the Gemini model
// @Tags Gemini
// @Accept json
// @Produce json
// @Param model path string true "Model ID"
// @Param request body dto.GeminiGenerateRequest true "Generate Request"
// @Success 200 {object} dto.GeminiGenerateResponse
// @Router /gemini/v1beta/models/{model}:generateContent [post]
func (h *GeminiController) HandleV1BetaGenerateContent(c fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	model := c.Params("model")
	var req dto.GeminiGenerateRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}
	if _, err := h.service.ResolveModel(model); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(err, "invalid_request_error"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	response, err := h.service.GenerateContent(ctx, model, req)
	if err != nil {
		if err.Error() == "empty content" {
			return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(err, "invalid_request_error"))
		}
		h.log.Error("GenerateContent failed", zap.Error(err), zap.String("model", model))
		return c.Status(fiber.StatusInternalServerError).JSON(common.ErrorToResponse(err, "api_error"))
	}

	return c.JSON(response)
}

// HandleV1BetaStreamGenerateContent handles the official Gemini streaming endpoint
// @Summary Stream Generate Content (Gemini)
// @Description Streams generated content using the Gemini model
// @Tags Gemini
// @Accept json
// @Produce json
// @Param model path string true "Model ID"
// @Param request body dto.GeminiGenerateRequest true "Generate Request"
// @Success 200 {string} string "Chunked JSON response"
// @Router /gemini/v1beta/models/{model}:streamGenerateContent [post]
func (h *GeminiController) HandleV1BetaStreamGenerateContent(c fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	model := c.Params("model")
	var req dto.GeminiGenerateRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}
	if _, err := h.service.ResolveModel(model); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(err, "invalid_request_error"))
	}

	useSSE := c.Query("alt") == "sse"
	if useSSE {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
	} else {
		c.Set("Content-Type", "application/x-ndjson")
	}

	c.RequestCtx().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		err := h.service.GenerateContentStream(ctx, model, req, func(resp dto.GeminiGenerateResponse) bool {
			if useSSE {
				return common.SendSSEEvent(w, h.log, resp)
			}
			return common.SendStreamChunk(w, h.log, resp) == nil
		})
		if err != nil {
			h.log.Error("GenerateContentStream failed", zap.Error(err), zap.String("model", model))
			if useSSE {
				_ = common.SendSSEEvent(w, h.log, common.ErrorToResponse(err, "api_error"))
			} else {
				_ = common.SendStreamChunk(w, h.log, common.ErrorToResponse(err, "api_error"))
			}
		}
	})

	return nil
}

// HandleDeepResearch handles a synchronous deep research request.
// @Summary Deep Research (synchronous)
// @Description Performs deep research on a topic using Gemini.
// @Tags Gemini
// @Accept json
// @Produce json
// @Param request body dto.DeepResearchRequest true "Deep Research Request"
// @Success 200 {object} dto.DeepResearchResponse
// @Router /gemini/v1beta/deepresearch [post]
func (h *GeminiController) HandleDeepResearch(c fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var req dto.DeepResearchRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}
	if req.Query == "" {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(fmt.Errorf("query field is required"), "invalid_request_error"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	response, err := h.service.DeepResearch(ctx, req)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(common.ErrorToResponse(err, "api_error"))
	}

	return c.JSON(response)
}

// HandleDeepResearchStream handles streaming deep research via SSE.
func (h *GeminiController) HandleDeepResearchStream(c fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var req dto.DeepResearchRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}
	if req.Query == "" {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(fmt.Errorf("query field is required"), "invalid_request_error"))
	}

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	c.RequestCtx().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		err := h.service.DeepResearchStream(ctx, req, func(ev dto.DeepResearchStreamEvent) bool {
			return common.SendSSEEvent(w, h.log, ev)
		})
		if err != nil {
			errEv := dto.DeepResearchStreamEvent{Event: "error", Error: err.Error()}
			_ = common.SendSSEEvent(w, h.log, errEv)
		}
	})

	return nil
}

// HandleInteractionCreate creates a deep research interaction.
func (h *GeminiController) HandleInteractionCreate(c fiber.Ctx) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var req dto.InteractionCreateRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}
	if req.Input == "" {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(fmt.Errorf("input field is required"), "invalid_request_error"))
	}

	drReq := dto.DeepResearchRequest{
		Query:      req.Input,
		Language:   req.Language,
		MaxSources: req.MaxSources,
		Images:     req.Images,
	}

	if req.Stream {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		c.RequestCtx().SetBodyStreamWriter(func(w *bufio.Writer) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			var partialText string
			_ = h.service.DeepResearchStream(ctx, drReq, func(ev dto.DeepResearchStreamEvent) bool {
				var resp dto.InteractionResponse
				switch ev.Event {
				case "step", "progress":
					resp = dto.InteractionResponse{Status: "in_progress", Query: req.Input}
					if partialText != "" {
						resp.Outputs = []dto.InteractionOutput{{Text: partialText}}
					}
				case "result":
					if ev.Result != nil {
						partialText = ev.Result.Summary
						resp = dto.InteractionResponse{
							ID: ev.Result.ID, Status: "completed", Query: req.Input,
							Outputs: []dto.InteractionOutput{{Text: ev.Result.Summary}},
							Sources: ev.Result.Sources, Steps: ev.Result.Steps,
							DurationMs: ev.Result.DurationMs, CreatedAt: ev.Result.CreatedAt, CompletedAt: ev.Result.CompletedAt,
						}
					}
				case "error":
					resp = dto.InteractionResponse{Status: "failed", Query: req.Input, Error: ev.Error}
				case "done":
					resp = dto.InteractionResponse{Status: "completed", Query: req.Input}
					_ = common.SendSSEEvent(w, h.log, resp)
					return false
				default:
					return true
				}
				return common.SendSSEEvent(w, h.log, resp)
			})
		})
		return nil
	}

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(common.ErrorToResponse(err, "internal_error"))
	}
	taskID := "task-" + hex.EncodeToString(b)
	task := &researchTask{ID: taskID, Status: taskStatusInProgress, Query: req.Input, CreatedAt: time.Now().Unix()}
	h.store.set(task)
	go h.backgroundResearch(taskID, drReq)

	return c.Status(fiber.StatusAccepted).JSON(taskToDTO(task))
}

// HandleInteractionGet polls status of a background research task.
func (h *GeminiController) HandleInteractionGet(c fiber.Ctx) error {
	id := c.Params("id")
	task, ok := h.store.get(id)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(common.ErrorToResponse(fmt.Errorf("task %q not found", id), "not_found"))
	}
	return c.JSON(taskToDTO(task))
}

// videoFileIDPattern finds the job ID in a files/{id}:download path. The last
// match is used because google-genai, given a non-https video URI, appends the
// whole URI to the path ("files/http://host/.../files/{id}:download…").
var videoFileIDPattern = regexp.MustCompile(`files/([a-z0-9]+):download`)

func videoRequestError(c fiber.Ctx, err error) error {
	var validationErr *providers.VideoValidationError
	var modelErr *providers.ModelSelectionError
	switch {
	case errors.As(err, &validationErr), errors.As(err, &modelErr):
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(err, "invalid_request_error"))
	case errors.Is(err, providers.ErrVideoBusy):
		return c.Status(fiber.StatusTooManyRequests).JSON(common.ErrorToResponse(err, "rate_limit_error"))
	case errors.Is(err, providers.ErrVideoJobNotFound):
		return c.Status(fiber.StatusNotFound).JSON(common.ErrorToResponse(err, "not_found"))
	case errors.Is(err, providers.ErrVideoNotReady):
		return c.Status(fiber.StatusConflict).JSON(common.ErrorToResponse(err, "invalid_request_error"))
	}
	return c.Status(fiber.StatusInternalServerError).JSON(common.ErrorToResponse(err, "internal_error"))
}

// HandlePredictLongRunning starts a text-to-video job (client.models.generate_videos)
// @Summary Generate videos (Gemini)
// @Description Starts an asynchronous text-to-video job using Gemini Web's Videos tool and returns a long-running operation. Poll the operation, then download the video file.
// @Tags Gemini
// @Accept json
// @Produce json
// @Param model path string true "Model ID (veo-* names use the account default)"
// @Param request body dto.PredictLongRunningRequest true "Video request"
// @Success 200 {object} dto.VideoOperation
// @Failure 400 {object} map[string]interface{}
// @Failure 429 {object} map[string]interface{}
// @Router /gemini/v1beta/models/{model}:predictLongRunning [post]
func (h *GeminiController) HandlePredictLongRunning(c fiber.Ctx) error {
	var req dto.PredictLongRunningRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(common.ErrorToResponse(fmt.Errorf("invalid request body: %w", err), "invalid_request_error"))
	}
	operation, err := h.service.StartVideoOperation(c.Params("model"), req)
	if err != nil {
		h.log.Warn("Start video operation failed", zap.Error(err))
		return videoRequestError(c, err)
	}
	return c.JSON(operation)
}

// HandleGetOperation polls a video operation (client.operations.get)
// @Summary Get video operation
// @Tags Gemini
// @Produce json
// @Param model path string true "Model ID"
// @Param operation path string true "Operation ID"
// @Success 200 {object} dto.VideoOperation
// @Failure 404 {object} map[string]interface{}
// @Router /gemini/v1beta/models/{model}/operations/{operation} [get]
func (h *GeminiController) HandleGetOperation(c fiber.Ctx) error {
	fileBaseURL := c.BaseURL() + "/gemini/v1beta/files"
	operation, err := h.service.GetVideoOperation(c.Params("model"), c.Params("operation"), fileBaseURL)
	if err != nil {
		return videoRequestError(c, err)
	}
	return c.JSON(operation)
}

// HandleDownloadFile serves a generated video (client.files.download)
// @Summary Download generated video
// @Tags Gemini
// @Produce video/mp4
// @Param file path string true "File ID"
// @Success 200 {file} binary
// @Failure 404 {object} map[string]interface{}
// @Failure 409 {object} map[string]interface{}
// @Router /gemini/v1beta/files/{file}:download [get]
func (h *GeminiController) HandleDownloadFile(c fiber.Ctx) error {
	matches := videoFileIDPattern.FindAllStringSubmatch(c.Path(), -1)
	if len(matches) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(common.ErrorToResponse(fmt.Errorf("file not found"), "not_found"))
	}
	id := matches[len(matches)-1][1]
	data, mimeType, err := h.service.VideoFile(id)
	if err != nil {
		return videoRequestError(c, err)
	}
	c.Set(fiber.HeaderContentType, mimeType)
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+id+`.mp4"`)
	return c.Send(data)
}

// Register registers the Gemini routes on the provided router
func (g *GeminiController) Register(group fiber.Router) {
	group.Post("/models/:model\\:predictLongRunning", g.HandlePredictLongRunning)
	group.Get("/models/:model/operations/:operation", g.HandleGetOperation)
	group.Get("/files/*", g.HandleDownloadFile)
	group.Get("/models", g.HandleV1BetaModels)
	group.Get("/models/:model", g.HandleV1BetaModel)
	group.Post("/models/:model\\:generateContent", g.HandleV1BetaGenerateContent)
	group.Post("/models/:model\\:streamGenerateContent", g.HandleV1BetaStreamGenerateContent)
	group.Post("/deepresearch", g.HandleDeepResearch)
	group.Post("/deepresearch/stream", g.HandleDeepResearchStream)
	group.Post("/interactions", g.HandleInteractionCreate)
	group.Get("/interactions/:id", g.HandleInteractionGet)
}

func (h *GeminiController) backgroundResearch(id string, req dto.DeepResearchRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	result, err := h.service.DeepResearch(ctx, req)
	if err != nil {
		h.store.update(id, func(t *researchTask) { t.Status = taskStatusFailed; t.Error = err.Error() })
		return
	}
	h.store.update(id, func(t *researchTask) { t.Status = taskStatusCompleted; t.Result = result })
}
