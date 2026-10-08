package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
)

const testCanvasHTML = "```html\n<!DOCTYPE html>\n<html><body>Todo</body></html>\n```"

// canvasTestEntry mirrors candidate[30][i] captured from Gemini Web.
func canvasTestEntry(code bool) []interface{} {
	entry := make([]interface{}, 18)
	if code {
		entry[0], entry[2], entry[4], entry[9], entry[10] = "c_test_index.html", "Simple To-Do List", testCanvasHTML, "index.html", 2.0
		entry[14] = []interface{}{testCanvasHTML, "html", nil, false, true}
	} else {
		entry[0], entry[2], entry[4], entry[9], entry[10] = "c_test_coffee_essay.md", "The Magic of Coffee", "The Magic of Coffee\n\nCoffee is great.", "coffee_essay.md", 1.0
		entry[14] = []interface{}{"The Magic of Coffee\n\nCoffee is great.", nil, []interface{}{}, nil, "markdown"}
	}
	entry[16] = true
	return entry
}

func canvasTestCandidate(text string, entries ...[]interface{}) []interface{} {
	candidate := make([]interface{}, 38)
	candidate[0] = "rc_test"
	candidate[1] = []interface{}{text}
	if len(entries) > 0 {
		list := make([]interface{}, len(entries))
		for i, entry := range entries {
			list[i] = entry
		}
		candidate[30] = list
	}
	return candidate
}

func TestExtractCanvases(t *testing.T) {
	canvases := extractCanvases(canvasTestCandidate("x", canvasTestEntry(true), canvasTestEntry(false)))
	if len(canvases) != 2 {
		t.Fatalf("canvases = %#v", canvases)
	}
	code, doc := canvases[0], canvases[1]
	if code.Type != CanvasCode || code.Language != "html" || code.FileName != "index.html" || code.Title != "Simple To-Do List" || code.Content != testCanvasHTML {
		t.Fatalf("code canvas = %#v", code)
	}
	if doc.Type != CanvasDocument || doc.Language != "markdown" || doc.FileName != "coffee_essay.md" {
		t.Fatalf("document canvas = %#v", doc)
	}
	if extractCanvases(canvasTestCandidate("no canvas")) != nil {
		t.Fatal("candidate without field 30 produced canvases")
	}
}

func TestRenderCanvasPlaceholders(t *testing.T) {
	canvases := []Canvas{{Type: CanvasCode, Language: "go", Content: "package main"}, {Type: CanvasDocument, Content: "# Essay"}}
	text := "Intro\n\nhttp://googleusercontent.com/immersive_entry_chip/0\n\nhttp://googleusercontent.com/immersive_entry_chip/1\n\nhttp://googleusercontent.com/immersive_entry_chip/7\nDone"
	want := "Intro\n\n```go\npackage main\n```\n\n# Essay\n\n\nDone"
	if got := renderCanvasPlaceholders(text, canvases); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanvasModelSuffix(t *testing.T) {
	for input, want := range map[string]string{"gemini-pro-canvas": "gemini-pro", "Gemini-Pro-CANVAS": "Gemini-Pro", "gemini-pro": "gemini-pro", "-canvas": "-canvas"} {
		got, _ := splitCanvasModel(input)
		if got != want {
			t.Fatalf("splitCanvasModel(%q) = %q, want %q", input, got, want)
		}
	}
	client := &Client{cachedModels: generationTestModels(), log: zap.NewNop()}
	info, err := client.ResolveModel("gemini-pro-canvas")
	if err != nil || info.ID != "gemini-pro-canvas" {
		t.Fatalf("ResolveModel = %#v, %v", info, err)
	}
}

func TestGenerateContentCanvasModeInlinesCanvas(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = generationTestTransport(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		var outer []interface{}
		_ = json.Unmarshal([]byte(r.PostForm.Get("f.req")), &outer)
		var inner []interface{}
		_ = json.Unmarshal([]byte(outer[1].(string)), &inner)
		if len(inner) != geminiWebClientInnerLength || inner[49] != float64(geminiCanvasToolMode) || inner[79] != float64(3) {
			t.Fatalf("not a canvas request: len=%d [49]=%v [79]=%v", len(inner), inner[49], inner[79])
		}
		var header []interface{}
		_ = json.Unmarshal([]byte(r.Header.Get(geminiModelHeaderKey)), &header)
		if features, _ := json.Marshal(header[8]); string(features) != "[4,5,6,8,16]" {
			t.Fatalf("model header features = %s", features)
		}
		// The canvas arrives in an earlier frame; the final frame has text only.
		text := "Here it is.\n\nhttp://googleusercontent.com/immersive_entry_chip/0\n\nEnjoy!"
		frame := func(candidate []interface{}) string {
			payload, _ := json.Marshal([]interface{}{nil, []interface{}{"c_test", "r_test"}, nil, nil, []interface{}{candidate}})
			line, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", nil, string(payload)}})
			return string(line)
		}
		body := ")]}'\n" + frame(canvasTestCandidate(text, canvasTestEntry(true))) + "\n" + frame(canvasTestCandidate(text)) + "\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})

	client := &Client{at: "test-token", cookieHeader: "c", cachedModels: generationTestModels(), language: "en", log: zap.NewNop()}
	response, err := client.GenerateContent(context.Background(), "Create a todo app", WithModel("gemini-pro-canvas"))
	if err != nil {
		t.Fatal(err)
	}
	if response.Model != "gemini-pro-canvas" || len(response.Canvases) != 1 {
		t.Fatalf("model=%q canvases=%d", response.Model, len(response.Canvases))
	}
	if !strings.Contains(response.Text, "```html\n<!DOCTYPE html>") || strings.Contains(response.Text, "immersive_entry_chip") {
		t.Fatalf("canvas not inlined: %q", response.Text)
	}
}

func TestCanvasFile(t *testing.T) {
	canvases := extractCanvases(canvasTestCandidate("x", canvasTestEntry(true), canvasTestEntry(false)))
	files := CanvasFiles(canvases)
	if len(files) != 2 {
		t.Fatalf("files = %#v", files)
	}
	code, doc := files[0], files[1]
	if code.Content != "<!DOCTYPE html>\n<html><body>Todo</body></html>" || code.MimeType != "text/html" || code.FileName != "index.html" || code.Type != CanvasCode {
		t.Fatalf("code file = %#v", code)
	}
	if doc.Content != "The Magic of Coffee\n\nCoffee is great." || doc.MimeType != "text/markdown" || doc.Type != CanvasDocument {
		t.Fatalf("document file = %#v", doc)
	}
	if CanvasFiles(nil) != nil {
		t.Fatal("no canvases must give nil, so JSON omits the field")
	}
}
