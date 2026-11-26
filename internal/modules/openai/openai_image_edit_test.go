package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
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

// TestImagesEditsReturnsURLWhenRequested covers response_format=url. The url
// must be an inline data: URL: the provider's own preview URL is only
// fetchable through the server-side signed chain and 403s for every client.
func TestImagesEditsReturnsURLWhenRequested(t *testing.T) {
	var captured providers.ImageEditRequest
	app := newImageEditTestApp(t, &captured, &providers.ImageEditResult{Data: []providers.Image{
		{URL: "https://lh3.googleusercontent.com/gg-dl/y", Generated: true, MimeType: "image/png",
			B64JSON: base64.StdEncoding.EncodeToString([]byte("img-bytes"))},
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
	url, _ := data["url"].(string)
	if url != "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("img-bytes")) {
		t.Errorf("url must be a data: URL carrying the bytes, got %.80q", url)
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

// --- mask (inpainting) ---

// newMaskedEditTestApp captures the provider-level masked request.
func newMaskedEditTestApp(t *testing.T, capture *providers.MaskedEditRequest, result *providers.ImageEditResult, err error) *fiber.App {
	t.Helper()
	client := providers.NewVideoTestClient([]providers.ModelInfo{{ID: "gemini-auto"}}, nil)
	client.SetMaskedEditFunc(func(_ context.Context, req providers.MaskedEditRequest) (*providers.ImageEditResult, error) {
		*capture = req
		return result, err
	})
	app := fiber.New()
	NewOpenAIController(NewOpenAIService(client, zap.NewNop()), zap.NewNop()).Register(app.Group("/openai/v1"))
	return app
}

func realPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func multipartWithFiles(t *testing.T, fields map[string]string, files []struct {
	field, name string
	data        []byte
}) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		part, err := w.CreateFormFile(f.field, f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

// TestImagesEditsWithMaskRoutesToMaskedEdit: when a `mask` part is present the
// request must take the masked path with image and mask kept distinct (they
// must NOT be flattened into the generic reference list, whose order contract
// is different), and the result must still be the OpenAI envelope.
func TestImagesEditsWithMaskRoutesToMaskedEdit(t *testing.T) {
	var captured providers.MaskedEditRequest
	app := newMaskedEditTestApp(t, &captured, &providers.ImageEditResult{Data: []providers.Image{
		{URL: "https://lh3.googleusercontent.com/gg-dl/m", Generated: true, B64JSON: base64.StdEncoding.EncodeToString([]byte("masked"))},
	}}, nil)

	base := realPNG(t, 16, 12)
	mask := realPNG(t, 16, 12)
	body, ctype := multipartWithFiles(t, map[string]string{
		"prompt":          "Replace the sky with stars",
		"model":           "gemini-auto",
		"response_format": "b64_json",
	}, []struct {
		field, name string
		data        []byte
	}{{"image", "photo.png", base}, {"mask", "mask.png", mask}})

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
	if string(captured.Image.Data) != string(base) || string(captured.Mask.Data) != string(mask) {
		t.Errorf("image and mask must arrive intact and distinct")
	}
	if captured.Image.Name != "photo.png" || captured.Mask.Name != "mask.png" {
		t.Errorf("names: image=%q mask=%q", captured.Image.Name, captured.Mask.Name)
	}
	if captured.Prompt != "Replace the sky with stars" || captured.Model != "gemini-auto" {
		t.Errorf("fields: %+v", captured)
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if d := out["data"].([]interface{})[0].(map[string]interface{}); d["b64_json"] == nil {
		t.Errorf("b64_json missing: %v", d)
	}
}

// TestImagesEditsWithMaskRejectsSizeMismatchBeforeProvider: a mask of the wrong
// size is a client error and must never reach Gemini (uploads are not free).
func TestImagesEditsWithMaskRejectsSizeMismatchBeforeProvider(t *testing.T) {
	called := false
	client := providers.NewVideoTestClient([]providers.ModelInfo{{ID: "gemini-auto"}}, nil)
	client.SetMaskedEditFunc(func(_ context.Context, req providers.MaskedEditRequest) (*providers.ImageEditResult, error) {
		called = true
		return nil, nil
	})
	app := fiber.New()
	NewOpenAIController(NewOpenAIService(client, zap.NewNop()), zap.NewNop()).Register(app.Group("/openai/v1"))

	body, ctype := multipartWithFiles(t, map[string]string{"prompt": "x"}, []struct {
		field, name string
		data        []byte
	}{{"image", "a.png", realPNG(t, 10, 10)}, {"mask", "m.png", realPNG(t, 9, 10)}})
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/images/edits", body)
	req.Header.Set("Content-Type", ctype)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400 for mask size mismatch, got %d", resp.StatusCode)
	}
	if called {
		t.Errorf("provider must not be called for an invalid mask")
	}
}

// TestImagesEditsWithMaskRequiresSingleImage: a mask addresses exactly one base
// image; several `image` parts plus a mask is ambiguous and must be a 400.
func TestImagesEditsWithMaskRequiresSingleImage(t *testing.T) {
	client := providers.NewVideoTestClient([]providers.ModelInfo{{ID: "gemini-auto"}}, nil)
	client.SetMaskedEditFunc(func(_ context.Context, req providers.MaskedEditRequest) (*providers.ImageEditResult, error) {
		t.Errorf("provider must not be called")
		return nil, nil
	})
	app := fiber.New()
	NewOpenAIController(NewOpenAIService(client, zap.NewNop()), zap.NewNop()).Register(app.Group("/openai/v1"))
	p := realPNG(t, 8, 8)
	body, ctype := multipartWithFiles(t, map[string]string{"prompt": "x"}, []struct {
		field, name string
		data        []byte
	}{{"image", "a.png", p}, {"image", "b.png", p}, {"mask", "m.png", p}})
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/images/edits", body)
	req.Header.Set("Content-Type", ctype)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400 for mask + multiple images, got %d", resp.StatusCode)
	}
}

// --- response_format=url must be usable by the caller ---

// buildDataURL is the shape we promise for response_format=url.
func TestBuildDataURL(t *testing.T) {
	got := buildDataURL("image/png", []byte{0x89, 0x50})
	if got != "data:image/png;base64,iVA=" {
		t.Fatalf("data url = %q", got)
	}
	if got := buildDataURL("", []byte{0xff, 0xd8, 0xff}); !strings.HasPrefix(got, "data:image/jpeg;base64,") {
		t.Fatalf("mime must be sniffed when missing, got %q", got)
	}
}
