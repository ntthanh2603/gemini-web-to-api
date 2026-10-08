package providers

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"gemini-web-to-api/internal/commons/models"
)

const (
	// geminiWebClientInnerLength matches the payload length sent by the web client.
	geminiWebClientInnerLength = 99
	// geminiCanvasToolMode is inner[49] when the "Canvas" tool is selected.
	geminiCanvasToolMode = 2
	// CanvasModelSuffix selects Canvas mode on any model, e.g. "gemini-pro-canvas".
	// The official Claude, OpenAI and Gemini APIs have no canvas parameter, so
	// the mode is chosen through the model name, like any other model variant.
	CanvasModelSuffix = "-canvas"
)

// Canvas kinds, from field 11 of a canvas entry.
const (
	CanvasDocument = "document"
	CanvasCode     = "code"
)

// Canvas is a document or code file Gemini wrote in its Canvas panel.
type Canvas struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Type     string `json:"type"`
	Language string `json:"language,omitempty"`
	Content  string `json:"content"`
}

var canvasPlaceholderRegex = regexp.MustCompile(`https?://googleusercontent\.com/immersive_entry_chip/(\d+)`)

// WithCanvas sends the request with Gemini Web's Canvas tool enabled.
func WithCanvas() GenerateOption {
	return func(c *GenerateConfig) { c.Canvas = true }
}

// splitCanvasModel strips the canvas suffix from a requested model name.
func splitCanvasModel(model string) (string, bool) {
	trimmed := strings.TrimSpace(model)
	if len(trimmed) > len(CanvasModelSuffix) && strings.HasSuffix(strings.ToLower(trimmed), CanvasModelSuffix) {
		return trimmed[:len(trimmed)-len(CanvasModelSuffix)], true
	}
	return model, false
}

// applyWebClientInner extends a StreamGenerate payload to the current web
// client's layout; tool modes (video, canvas) are set on top of it.
func applyWebClientInner(inner []interface{}) []interface{} {
	if len(inner) < geminiWebClientInnerLength {
		extended := make([]interface{}, geminiWebClientInnerLength)
		copy(extended, inner)
		inner = extended
	}
	inner[30] = []interface{}{4, 16}
	inner[68] = 2
	inner[91] = 0
	inner[96] = 0
	inner[98] = 1
	return inner
}

// applyCanvasInner selects the Canvas tool.
func applyCanvasInner(inner []interface{}) []interface{} {
	inner = applyWebClientInner(inner)
	inner[49] = geminiCanvasToolMode
	inner[67] = 0
	return inner
}

// extractCanvases reads candidate[30]: one entry per canvas, laid out as
// [id, _, title, _, content, …, fileName(9), kind(10), …, details(14), …].
// Code details are [content, language, …]; document details carry the
// language at index 4.
func extractCanvases(candidate []interface{}) []Canvas {
	if len(candidate) <= 30 {
		return nil
	}
	entries, _ := candidate[30].([]interface{})
	var canvases []Canvas
	for _, raw := range entries {
		entry, ok := raw.([]interface{})
		if !ok || len(entry) < 11 {
			continue
		}
		content, _ := entry[4].(string)
		if content == "" {
			continue
		}
		canvas := Canvas{Content: content, Type: CanvasDocument}
		canvas.ID, _ = entry[0].(string)
		canvas.Title, _ = entry[2].(string)
		canvas.FileName, _ = entry[9].(string)
		if kind, _ := entry[10].(float64); kind == 2 {
			canvas.Type = CanvasCode
		}
		if len(entry) > 14 {
			if details, ok := entry[14].([]interface{}); ok {
				for _, index := range []int{1, 4} {
					if index < len(details) {
						if language, ok := details[index].(string); ok && language != "" {
							canvas.Language = language
							break
						}
					}
				}
			}
		}
		if canvas.Language == "" {
			canvas.Language = strings.TrimPrefix(path.Ext(canvas.FileName), ".")
		}
		canvases = append(canvases, canvas)
	}
	return canvases
}

var canvasFenceRegex = regexp.MustCompile("(?s)^```[^\\n]*\\n(.*?)\\n?```$")

// Source returns the raw file content, without a Markdown code fence.
func (c Canvas) Source() string {
	content := strings.TrimSpace(c.Content)
	if match := canvasFenceRegex.FindStringSubmatch(content); match != nil {
		return match[1]
	}
	return content
}

var canvasMimeTypes = map[string]string{
	"html": "text/html", "css": "text/css", "javascript": "text/javascript", "js": "text/javascript",
	"typescript": "text/x-typescript", "ts": "text/x-typescript", "jsx": "text/jsx", "tsx": "text/tsx",
	"python": "text/x-python", "py": "text/x-python", "java": "text/x-java", "go": "text/x-go",
	"c": "text/x-c", "cpp": "text/x-c++", "csharp": "text/x-csharp", "rust": "text/x-rust",
	"sql": "application/sql", "json": "application/json", "xml": "application/xml", "yaml": "application/yaml",
	"markdown": "text/markdown", "md": "text/markdown", "shell": "text/x-shellscript", "bash": "text/x-shellscript",
}

// MimeType guesses the media type from the canvas language and file name.
func (c Canvas) MimeType() string {
	for _, key := range []string{strings.ToLower(c.Language), strings.TrimPrefix(strings.ToLower(path.Ext(c.FileName)), ".")} {
		if mimeType, ok := canvasMimeTypes[key]; ok {
			return mimeType
		}
	}
	if c.Type == CanvasDocument {
		return "text/markdown"
	}
	return "text/plain"
}

// File converts a canvas to its API representation.
func (c Canvas) File() models.CanvasFile {
	fileName := c.FileName
	if fileName == "" {
		fileName = c.ID
	}
	return models.CanvasFile{ID: c.ID, Title: c.Title, FileName: fileName, Type: c.Type, Language: c.Language, MimeType: c.MimeType(), Content: c.Source()}
}

// CanvasFiles converts canvases to their API representation (nil when empty).
func CanvasFiles(canvases []Canvas) []models.CanvasFile {
	if len(canvases) == 0 {
		return nil
	}
	files := make([]models.CanvasFile, len(canvases))
	for i, canvas := range canvases {
		files[i] = canvas.File()
	}
	return files
}

// Markdown renders a canvas the way the official APIs return generated
// files: documents as Markdown, code as a fenced block.
func (c Canvas) Markdown() string {
	content := strings.TrimSpace(c.Content)
	if c.Type != CanvasCode || strings.HasPrefix(content, "```") {
		return content
	}
	return "```" + c.Language + "\n" + content + "\n```"
}

// renderCanvasPlaceholders replaces Gemini's immersive_entry_chip links with
// the canvas content they stand for, and drops links without a canvas.
func renderCanvasPlaceholders(text string, canvases []Canvas) string {
	if !canvasPlaceholderRegex.MatchString(text) {
		return text
	}
	return canvasPlaceholderRegex.ReplaceAllStringFunc(text, func(match string) string {
		index, err := strconv.Atoi(canvasPlaceholderRegex.FindStringSubmatch(match)[1])
		if err != nil || index < 0 || index >= len(canvases) {
			return ""
		}
		return canvases[index].Markdown()
	})
}
