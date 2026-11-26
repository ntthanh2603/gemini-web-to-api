package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gemini-web-to-api/internal/modules/providers"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

const testPNG = "\x89PNG\r\n\x1a\nfake"

// newImageEditTestApp wires the real controller + service to a client whose
// image-edit function is captured, so the test asserts both on the parsed
// request (what reached the provider) and on the HTTP response shape.
func newImageEditTestApp(t *testing.T, capture *providers.ImageEditRequest, result *providers.ImageEditResult, err error) *fiber.App {
	t.Helper()
	client := providers.NewVideoTestClient([]providers.ModelInfo{{ID: "gemini-auto"}}, nil)
	client.SetImageEditFunc(func(_ context.Context, req providers.ImageEditRequest) (*providers.ImageEditResult, error) {
		*capture = req
		return result, err
	})
	app := fiber.New()
	NewOpenAIController(NewOpenAIService(client, zap.NewNop()), zap.NewNop()).Register(app.Group("/openai/v1"))
	return app
}

func multipartEditBody(t *testing.T, fields map[string]string, images map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range images {
		// OpenAI clients send image parts named "image" (repeated) or "image[]".
		part, err := w.CreateFormFile(name, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

// TestImagesEditsParsesMultipartAndReturnsOpenAIShape is the contract test for
// the endpoint: standard OpenAI multipart in, standard OpenAI image response out.
func TestImagesEditsParsesMultipartAndReturnsOpenAIShape(t *testing.T) {
	var captured providers.ImageEditRequest
	app := newImageEditTestApp(t, &captured, &providers.ImageEditResult{Created: 1700000000, Data: []providers.Image{
		{URL: "https://lh3.googleusercontent.com/gg-dl/x", Generated: true, B64JSON: base64.StdEncoding.EncodeToString([]byte("img-bytes"))},
	}}, nil)

	imgA, imgB := []byte("\x89PNG\r\n\x1a\n-a"), []byte("\x89PNG\r\n\x1a\n-b")
	body, ctype := multipartEditBody(t, map[string]string{
		"prompt":          "Put a red hat on the cat",
		"model":           "gemini-auto",
		"n":               "2",
		"size":            "1536x1024",
		"response_format": "b64_json",
	}, map[string][]byte{"image": imgA, "image[]": imgB})

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/images/edits", body)
	req.Header.Set("Content-Type", ctype)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}

	// What reached the provider
	if captured.Prompt != "Put a red hat on the cat" || captured.Model != "gemini-auto" || captured.Size != "1536x1024" {
		t.Errorf("request fields lost: %+v", captured)
	}
	if len(captured.Files) != 2 {
		t.Fatalf("want 2 reference images, got %d", len(captured.Files))
	}
	if string(captured.Files[0].Data) != string(imgA) || string(captured.Files[1].Data) != string(imgB) {
		t.Errorf("image order/bytes changed: %q %q", captured.Files[0].Data, captured.Files[1].Data)
	}
	for _, f := range captured.Files {
		if !strings.HasPrefix(f.MimeType, "image/") {
			t.Errorf("mime not detected for %q: %q", f.Name, f.MimeType)
		}
	}

	// What the client gets back
	var out struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// n=2 with a stub that returns one image per call means two independent
	// generations (Gemini Web has no batch edit), so two data items.
	if len(out.Data) != 2 {
		t.Fatalf("want 2 data items for n=2, got %d", len(out.Data))
	}
	for i, item := range out.Data {
		if item.B64JSON == "" {
			t.Errorf("data[%d]: b64_json requested but missing", i)
		}
		if decoded, _ := base64.StdEncoding.DecodeString(item.B64JSON); string(decoded) != "img-bytes" {
			t.Errorf("data[%d]: b64_json payload corrupted: %q", i, decoded)
		}
		if item.URL != "" {
			t.Errorf("data[%d]: url must be omitted when b64_json is requested, got %q", i, item.URL)
		}
	}
}

// TestImagesEditsReturnsURLWhenRequested covers response_format=url, which the
// OpenAI SDK also sends; then the url field must be populated and b64_json absent.
func TestImagesEditsReturnsURLWhenRequested(t *testing.T) {
	var captured providers.ImageEditRequest
	app := newImageEditTestApp(t, &captured, &providers.ImageEditResult{Created: 1, Data: []providers.Image{
		{URL: "https://lh3.googleusercontent.com/gg-dl/y", Generated: true},
	}}, nil)
	body, ctype := multipartEditBody(t, map[string]string{"prompt": "edit", "response_format": "url"}, map[string][]byte{"image": []byte(testPNG)})
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/images/edits", body)
	req.Header.Set("Content-Type", ctype)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	data := out["data"].([]interface{})[0].(map[string]interface{})
	if data["url"] != "https://lh3.googleusercontent.com/gg-dl/y" {
		t.Errorf("url = %v", data["url"])
	}
	if _, has := data["b64_json"]; has {
		t.Errorf("b64_json must be omitted for response_format=url")
	}
}

// TestImagesEditsRejectsMissingImageOrPrompt: the OpenAI API requires both and
// answers 400; clients rely on that to validate input before uploading.
func TestImagesEditsRejectsMissingImageOrPrompt(t *testing.T) {
	var captured providers.ImageEditRequest
	app := newImageEditTestApp(t, &captured, nil, nil)
	cases := []struct {
		name   string
		fields map[string]string
		images map[string][]byte
	}{
		{"no image", map[string]string{"prompt": "edit"}, nil},
		{"no prompt", nil, map[string][]byte{"image": []byte(testPNG)}},
	}
	for _, tc := range cases {
		body, ctype := multipartEditBody(t, tc.fields, tc.images)
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/images/edits", body)
		req.Header.Set("Content-Type", ctype)
		resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", tc.name, resp.StatusCode)
		}
	}
	if captured.Prompt != "" {
		t.Errorf("provider must not be called when validation fails, got %+v", captured)
	}
}

// TestImagesEditsMapsProviderErrorLikeGenerations keeps the error contract the
// text-to-image endpoint already uses (openAIRequestError: unknown provider
// failures are 500 api_error).
func TestImagesEditsMapsProviderErrorLikeGenerations(t *testing.T) {
	var captured providers.ImageEditRequest
	app := newImageEditTestApp(t, &captured, nil, context.DeadlineExceeded)
	body, ctype := multipartEditBody(t, map[string]string{"prompt": "edit"}, map[string][]byte{"image": []byte(testPNG)})
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/images/edits", body)
	req.Header.Set("Content-Type", ctype)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("want 500 for provider failure (upstream convention), got %d", resp.StatusCode)
	}
}
