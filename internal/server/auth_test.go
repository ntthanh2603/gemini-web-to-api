package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gemini-web-to-api/internal/commons/configs"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

// newAuthTestApp builds the real app shell (CORS, auth, rate limit) and adds
// stub routes in place of the business modules, so the auth middleware is
// exercised exactly as it is wired in production.
func newAuthTestApp(apiKey string) *fiber.App {
	cfg := minimalAuthConfig(apiKey)
	app := NewGeminiWebToAPI(nopLogger(), cfg)
	app.Get("/openai/v1/models", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Post("/openai/v1/images/generations", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/claude/v1/models", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/gemini/v1beta/models", func(c fiber.Ctx) error { return c.SendString("ok") })
	return app
}

func TestAPIKeyAuthDisabledWhenUnset(t *testing.T) {
	app := newAuthTestApp("")
	resp, _ := testRequest(app, http.MethodGet, "/openai/v1/models", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("requests must pass without auth when API_KEY is unset, got %d", resp.StatusCode)
	}
}

func TestAPIKeyAuthAcceptsProtocolHeaders(t *testing.T) {
	app := newAuthTestApp("secret-key")
	cases := map[string]string{
		// OpenAI style
		"Authorization": "Bearer secret-key",
		// Claude style
		"X-Api-Key": "secret-key",
		// Gemini style
		"x-goog-api-key": "secret-key",
		// ?key= query (Gemini SDKs send it this way)
	}
	for header, value := range cases {
		req := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
		req.Header.Set(header, value)
		resp, _ := app.Test(req, testTimeout)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s must be accepted, got %d", header, resp.StatusCode)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/openai/v1/models?key=secret-key", nil)
	if resp, _ := app.Test(req, testTimeout); resp.StatusCode != http.StatusOK {
		t.Fatalf("?key= must be accepted, got %d", resp.StatusCode)
	}
}

func TestAPIKeyAuthProtectsEveryProtocolGroup(t *testing.T) {
	app := newAuthTestApp("secret-key")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/openai/v1/images/generations"},
		{http.MethodGet, "/claude/v1/models"},
		{http.MethodGet, "/gemini/v1beta/models"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if resp, _ := app.Test(req, testTimeout); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without key: want 401, got %d", tc.method, tc.path, resp.StatusCode)
		}
		req = httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer secret-key")
		if resp, _ := app.Test(req, testTimeout); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s with key: want 200, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestAPIKeyAuthRejectsMissingOrWrongKey(t *testing.T) {
	app := newAuthTestApp("secret-key")
	for _, tc := range []struct {
		name, header, value string
	}{
		{"no header", "", ""},
		{"wrong bearer", "Authorization", "Bearer nope"},
		{"wrong x-api-key", "X-Api-Key", "nope"},
		{"wrong query", "", ""},
	} {
		req := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
		if tc.header != "" {
			req.Header.Set(tc.header, tc.value)
		}
		if tc.name == "wrong query" {
			req = httptest.NewRequest(http.MethodGet, "/openai/v1/models?key=nope", nil)
		}
		resp, _ := app.Test(req, testTimeout)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d", tc.name, resp.StatusCode)
		}
	}
}

func TestAPIKeyAuthKeepsPublicEndpointsOpen(t *testing.T) {
	app := newAuthTestApp("secret-key")
	for _, path := range []string{"/health", "/docs", "/openapi.json"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp, _ := app.Test(req, testTimeout)
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatalf("%s must stay public", path)
		}
	}
}

// --- test helpers (local to this file; keep minimal, no external deps) ---

var testTimeout = fiber.TestConfig{Timeout: 2 * time.Second}

func minimalAuthConfig(apiKey string) *configs.Config {
	return &configs.Config{
		Server:    configs.ServerConfig{Port: "0", APIKey: apiKey},
		RateLimit: configs.RateLimitConfig{},
		LogLevel:  "error",
	}
}

func nopLogger() *zap.Logger { return zap.NewNop() }

func testRequest(app *fiber.App, method, path, body string) (*http.Response, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	return app.Test(httptest.NewRequest(method, path, reader), testTimeout)
}

// TestAPIKeyAuthAcceptsAdminCookieKeyForAdminRoute guards against a conflict
// between the two secrets: /admin/cookies reads GEMINI_COOKIE_API_KEY from the
// same Authorization header the global middleware checks. Once API_KEY is set
// the middleware must accept that key too, or the admin endpoint can never be
// reached. The admin route still verifies its own key afterwards.
//
// The config is built through configs.New() because the second key is derived
// there; hand-building the struct would not exercise the real wiring.
func TestAPIKeyAuthAcceptsAdminCookieKeyForAdminRoute(t *testing.T) {
	t.Setenv("API_KEY", "api-key")
	t.Setenv("GEMINI_COOKIE_API_KEY", "cookie-key")
	t.Setenv("GEMINI_COOKIES", "__Secure-1PSID=psid; __Secure-1PSIDTS=psidts")
	cfg, err := configs.New()
	if err != nil {
		t.Fatal(err)
	}
	app := NewGeminiWebToAPI(nopLogger(), cfg)
	app.Post("/admin/cookies", func(c fiber.Ctx) error { return c.SendString("reached admin") })

	req := httptest.NewRequest(http.MethodPost, "/admin/cookies", nil)
	req.Header.Set("Authorization", "Bearer cookie-key")
	if resp, _ := app.Test(req, testTimeout); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin key must pass the global middleware, got %d", resp.StatusCode)
	}
	// ...but an unrelated key still must not.
	req = httptest.NewRequest(http.MethodPost, "/admin/cookies", nil)
	req.Header.Set("Authorization", "Bearer neither")
	if resp, _ := app.Test(req, testTimeout); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown key must be rejected, got %d", resp.StatusCode)
	}
}
