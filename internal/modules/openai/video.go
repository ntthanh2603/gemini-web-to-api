package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"gemini-web-to-api/internal/modules/providers"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"go.uber.org/fx"
)

type videoBackend interface {
	IsHealthy() bool
	ResolveModel(string) (providers.ModelInfo, error)
	GenerateVideo(context.Context, string, string, ...providers.VideoOption) (*providers.Video, error)
	DownloadVideo(context.Context, providers.Video, io.Writer) error
}
type videoRequest struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model,omitempty"`
}
type videoJob struct {
	ID              string                `json:"id"`
	Object          string                `json:"object"`
	Status          string                `json:"status"`
	Model           string                `json:"model"`
	CreatedAt       int64                 `json:"created_at"`
	ExpiresAt       int64                 `json:"expires_at"`
	ContentURL      string                `json:"content_url,omitempty"`
	Error           *providers.VideoError `json:"error,omitempty"`
	ConversationID  string                `json:"conversation_id,omitempty"`
	ProviderMessage string                `json:"provider_message,omitempty"`
	path            string
}

// Jobs and files are process-local, bounded, and removed after one hour. A
// single active generation avoids unexpectedly exhausting a shared account.
type videoController struct {
	backend   videoBackend
	mu        sync.Mutex
	jobs      map[string]videoJob
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	dir       string
	closed    bool
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
}

func newVideoController(backend videoBackend) *videoController {
	ctx, cancel := context.WithCancel(context.Background())
	return &videoController{backend: backend, jobs: make(map[string]videoJob), closeDone: make(chan struct{}), ctx: ctx, cancel: cancel}
}
func registerVideoRoutes(app *fiber.App, client *providers.Client, lc fx.Lifecycle) {
	v := newVideoController(client)
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		v.wg.Add(1)
		go func() {
			defer v.wg.Done()
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-v.ctx.Done():
					return
				case <-ticker.C:
					v.mu.Lock()
					v.expireLocked()
					v.mu.Unlock()
				}
			}
		}()
		return nil
	}, OnStop: func(ctx context.Context) error { return v.close(ctx) }})
	v.register(app.Group("/openai/v1"))
}
func (v *videoController) register(r fiber.Router) {
	r.Post("/videos", v.create)
	r.Get("/videos/:id", v.get)
	r.Get("/videos/:id/content", v.content)
}
func (v *videoController) expireLocked() {
	for id, job := range v.jobs {
		if job.Status != "in_progress" && job.ExpiresAt <= time.Now().Unix() {
			if job.path != "" {
				if err := os.Remove(job.path); err != nil && !os.IsNotExist(err) {
					continue
				}
			}
			delete(v.jobs, id)
		}
	}
}
func videoAPIError(c fiber.Ctx, status int, code, message string) error {
	return c.Status(status).JSON(fiber.Map{"error": &providers.VideoError{Code: code, Message: message}})
}
func (v *videoController) create(c fiber.Ctx) error {
	if !v.backend.IsHealthy() {
		return videoAPIError(c, 503, "provider_unavailable", "Gemini is not initialized; check the configured session and server startup")
	}
	var req videoRequest
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil {
		return videoAPIError(c, 400, "invalid_request", "Expected JSON with prompt and optional model; other options are not supported")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return videoAPIError(c, 400, "invalid_request", "Expected one JSON object")
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" || len(req.Prompt) > 8000 {
		return videoAPIError(c, 400, "invalid_request", "prompt must contain 1 to 8000 bytes")
	}
	model, err := v.backend.ResolveModel(req.Model)
	if err != nil {
		return videoAPIError(c, 400, "invalid_model", "Select a Gemini model from /openai/v1/models")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.expireLocked()
	if v.closed {
		return videoAPIError(c, 503, "shutting_down", "Server is shutting down")
	}
	for _, job := range v.jobs {
		if job.Status == "in_progress" {
			return videoAPIError(c, 429, "video_busy", "A video is already generating; poll that job before starting another")
		}
	}
	if len(v.jobs) >= 10 {
		return videoAPIError(c, 429, "video_storage_full", "Video job storage is full; completed jobs expire after one hour")
	}
	if v.dir == "" {
		v.dir, err = os.MkdirTemp("", "gemini-videos-")
		if err != nil {
			return videoAPIError(c, 500, "storage_error", "Could not create private video storage")
		}
	}
	now := time.Now().Unix()
	job := videoJob{ID: "video_" + uuid.NewString(), Object: "video", Status: "in_progress", Model: model.ID, CreatedAt: now, ExpiresAt: now + 3600}
	v.jobs[job.ID] = job
	v.wg.Add(1)
	go v.run(job, req.Prompt)
	c.Set("Location", "/openai/v1/videos/"+job.ID)
	c.Set("Cache-Control", "no-store")
	return c.Status(202).JSON(job)
}

// Temporary-directory cleaners may remove an idle cache while the server is
// still running. Allocate a fresh private directory only for a missing path;
// permission and disk errors must still fail before contacting the provider.
func (v *videoController) reserveVideoFile() (*os.File, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, context.Canceled
	}
	if v.dir != "" {
		file, err := os.CreateTemp(v.dir, "*.mp4")
		if err == nil || !os.IsNotExist(err) {
			return file, err
		}
		// Windows also reports a file used as a directory as path-not-found.
		// Recover only when the cache directory itself has disappeared.
		if _, statErr := os.Stat(v.dir); !os.IsNotExist(statErr) {
			return nil, err
		}
	}
	directory, err := os.MkdirTemp("", "gemini-videos-")
	if err != nil {
		return nil, err
	}
	v.dir = directory
	return os.CreateTemp(v.dir, "*.mp4")
}

func (v *videoController) run(job videoJob, prompt string) {
	defer v.wg.Done()
	ctx, cancel := context.WithTimeout(v.ctx, 10*time.Minute)
	defer cancel()
	var file *os.File
	var err error
	defer func() {
		if recover() != nil {
			err = &providers.VideoError{Code: "internal_error", Message: "Video worker failed unexpectedly; check Gemini Web before retrying"}
		}
		if file != nil {
			if closeErr := file.Close(); err == nil && closeErr != nil {
				err = &providers.VideoError{Code: "storage_error", Message: "Could not finish saving the video"}
			}
		}
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		job.ExpiresAt = time.Now().Add(time.Hour).Unix()
		if err != nil {
			if job.path != "" {
				if removeErr := os.Remove(job.path); removeErr == nil || os.IsNotExist(removeErr) {
					job.path = ""
				}
			}
			job.Status = "failed"
			var safe *providers.VideoError
			if errors.As(err, &safe) {
				copy := *safe
				job.Error = &copy
			} else {
				job.Error = &providers.VideoError{Code: "video_failed", Message: "Video generation or storage failed; check Gemini Web before retrying"}
			}
		} else {
			job.Status = "completed"
			job.ContentURL = "/openai/v1/videos/" + job.ID + "/content"
		}
		v.mu.Lock()
		v.jobs[job.ID] = job
		v.mu.Unlock()
	}()
	// Reserve a writable file before consuming upstream generation quota.
	file, err = v.reserveVideoFile()
	if err != nil {
		err = &providers.VideoError{Code: "storage_error", Message: "Could not reserve video storage"}
		return
	}
	job.path = file.Name()
	result, generationErr := v.backend.GenerateVideo(ctx, prompt, job.Model, providers.WithVideoProgress(func(progress providers.VideoProgress) {
		job.ConversationID = progress.ConversationID
		job.ProviderMessage = progress.Message
		v.mu.Lock()
		v.jobs[job.ID] = job
		v.mu.Unlock()
	}))
	err = generationErr
	if err != nil {
		return
	}
	if result == nil {
		err = errors.New("empty video")
		return
	}
	if ctx.Err() != nil {
		err = ctx.Err()
		return
	}
	err = v.backend.DownloadVideo(ctx, *result, file)
}
func (v *videoController) get(c fiber.Ctx) error {
	v.mu.Lock()
	v.expireLocked()
	job, ok := v.jobs[c.Params("id")]
	v.mu.Unlock()
	if !ok || (job.Status != "in_progress" && job.ExpiresAt <= time.Now().Unix()) {
		return videoAPIError(c, 404, "not_found", "Video job not found or expired")
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(job)
}
func (v *videoController) content(c fiber.Ctx) error {
	v.mu.Lock()
	v.expireLocked()
	job, ok := v.jobs[c.Params("id")]
	if !ok || (job.Status != "in_progress" && job.ExpiresAt <= time.Now().Unix()) {
		v.mu.Unlock()
		return videoAPIError(c, 404, "not_found", "Video job not found or expired")
	}
	if job.Status != "completed" {
		v.mu.Unlock()
		return videoAPIError(c, 409, "video_not_ready", "Video job has not completed successfully")
	}
	f, err := os.Open(job.path)
	v.mu.Unlock()
	if err != nil {
		return videoAPIError(c, 410, "content_unavailable", "Video file is no longer available")
	}
	c.Set("Content-Type", "video/mp4")
	c.Set("Cache-Control", "no-store")
	c.Set("Content-Disposition", `attachment; filename="`+job.ID+`.mp4"`)
	return c.SendStream(f)
}
func (v *videoController) close(ctx context.Context) error {
	v.closeOnce.Do(func() {
		v.mu.Lock()
		v.closed = true
		v.cancel()
		v.mu.Unlock()
		go func() {
			v.wg.Wait()
			if v.dir != "" {
				v.closeErr = os.RemoveAll(v.dir)
			}
			close(v.closeDone)
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-v.closeDone:
		return v.closeErr
	}
}
