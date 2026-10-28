package admin

import (
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"gemini-web-to-api/internal/commons/configs"
	"gemini-web-to-api/internal/modules/providers"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

// cookieUpdateRequest is the body of POST /admin/cookies. It carries the same
// complete Cookie header shape GEMINI_COOKIES expects.
type cookieUpdateRequest struct {
	Cookies string `json:"cookies"`
}

// Controller serves the authenticated cookie maintenance endpoints used by
// browser extensions to push a fresh Gemini session.
type Controller struct {
	client *providers.Client
	apiKey string
	log    *zap.Logger
}

func NewController(client *providers.Client, cfg *configs.Config, log *zap.Logger) *Controller {
	return &Controller{
		client: client,
		apiKey: strings.TrimSpace(cfg.Gemini.CookieAPIKey),
		log:    log,
	}
}

// Enabled reports whether GEMINI_COOKIE_API_KEY is set.
func (h *Controller) Enabled() bool { return h.apiKey != "" }

// Register mounts the endpoints. When no API key is configured the routes are
// not registered at all, so the server behaves exactly as before and callers
// get the normal 404 handler.
func (h *Controller) Register(app *fiber.App) {
	if !h.Enabled() {
		h.log.Info("Cookie update endpoint disabled; set GEMINI_COOKIE_API_KEY to enable /admin/cookies")
		return
	}

	app.Post("/admin/cookies", h.HandleUpdateCookies)
	app.Get("/admin/cookies", h.HandleCookieStatus)
	h.log.Info("Cookie update endpoint enabled", zap.String("path", "/admin/cookies"))
}

// HandleUpdateCookies accepts a complete Cookie header, validates it the same
// way startup does and swaps it in only when Google accepts the session.
func (h *Controller) HandleUpdateCookies(c fiber.Ctx) error {
	if !h.authorize(c) {
		return nil
	}

	var req cookieUpdateRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid JSON body: expected {\"cookies\": \"<Cookie header>\"}",
		})
	}
	if strings.TrimSpace(req.Cookies) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "cookies is required",
		})
	}

	result, err := h.client.UpdateCookies(c.Context(), req.Cookies)
	if err != nil {
		return h.writeUpdateError(c, err)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"status":     string(result.Status),
		"source":     result.Source,
		"updated_at": formatTime(result.UpdatedAt),
	})
}

// HandleCookieStatus reports which session is live without disclosing it.
func (h *Controller) HandleCookieStatus(c fiber.Ctx) error {
	if !h.authorize(c) {
		return nil
	}

	status := h.client.CookieStatus()
	if status.Fingerprint == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "no active Gemini session",
		})
	}

	ageSeconds := int64(status.CacheAge.Seconds())
	if ageSeconds < 0 {
		ageSeconds = 0
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"psid_fingerprint":  status.Fingerprint,
		"cache_age_seconds": ageSeconds,
		"updated_at":        formatTime(status.UpdatedAt),
		"source":            status.Source,
	})
}

// writeUpdateError maps provider errors onto short, secret-free responses.
func (h *Controller) writeUpdateError(c fiber.Ctx, err error) error {
	var invalid *providers.CookieValidationError
	if errors.As(err, &invalid) {
		h.log.Info("Rejected cookie update", zap.String("reason", invalid.Reason))
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": invalid.Reason})
	}

	var check *providers.CookieSessionCheckError
	if errors.As(err, &check) {
		h.log.Warn("Could not verify pushed cookies with Google", zap.Error(check))
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "could not verify cookies with Google; the current session was left unchanged",
		})
	}

	h.log.Error("Unexpected cookie update failure", zap.Error(err))
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "cookie update failed"})
}

// authorize enforces the bearer token with a constant-time comparison. It
// reports whether the request may proceed; on failure the 401 response has
// already been written.
func (h *Controller) authorize(c fiber.Ctx) bool {
	if token, ok := bearerToken(c.Get("Authorization")); ok {
		if subtle.ConstantTimeCompare([]byte(token), []byte(h.apiKey)) == 1 {
			return true
		}
	}

	// Info, not Warn: anyone who can reach the port can trigger this, and the
	// development logger attaches a stack trace to every warning.
	h.log.Info("Rejected unauthenticated cookie admin request", zap.String("remote_ip", c.IP()))
	_ = c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	return false
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
