package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gemini-web-to-api/internal/modules/providers"

	"github.com/gofiber/fiber/v3"
)

func statusFor(t *testing.T, err error) int {
	t.Helper()
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error { return openAIRequestError(c, err) })
	resp, testErr := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if testErr != nil {
		t.Fatalf("app.Test: %v", testErr)
	}
	return resp.StatusCode
}

func TestOpenAIRequestErrorDeadSessionIs503(t *testing.T) {
	err := &providers.ModelSelectionError{Message: "no Gemini models available", Unavailable: true}
	if got := statusFor(t, err); got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", got)
	}
}

func TestOpenAIRequestErrorUnknownModelStays400(t *testing.T) {
	err := &providers.ModelSelectionError{Message: "unsupported Gemini model"}
	if got := statusFor(t, err); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
}
