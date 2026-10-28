package utils

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gemini-web-to-api/internal/commons/models"

	"go.uber.org/zap"
)

// Web-UI artifact patterns. They are intentionally narrow so that normal content
// is never rewritten: only numeric citation lists, the [cite_start] marker and
// code-event query suffixes attached to a markdown code-fence opening line.
var (
	// citationMarkerRe matches "[cite: 3]" and "[cite: 3, 7]".
	citationMarkerRe = regexp.MustCompile(`\[cite:[ 	]*\d+(?:[ 	]*,[ 	]*\d+)*\]`)
	// citeStartMarkerRe matches the "[cite_start]" marker preceding a cited passage.
	citeStartMarkerRe = regexp.MustCompile(`\[cite_start\]`)
	// codeFenceArtifactRe matches a code-fence opening line carrying a web-UI
	// code-event suffix, e.g. "```python?code_reference&code_event_index=1" or
	// "```text?code_stdout&code_event_index=1". The suffix is dropped, the fence
	// and its language tag are kept.
	codeFenceArtifactRe = regexp.MustCompile("^([ 	]*```[^\\s?]*)\\?(?:code_reference|code_stdout)(?:&[A-Za-z0-9_.=&%~-]*)?[ 	\r]*$")
)

// EstimateTokens approximates a SentencePiece token count for text when the
// upstream wire carries no usage metadata. It is rune-based with per-script
// weighting: CJK runes (Han, Hiragana, Katakana, Hangul) count ~1 token each,
// every other rune counts ~1/4 token. It replaces the old len(bytes)/4
// heuristic, which overcounts multi-byte Latin scripts such as Vietnamese and
// undercounts CJK (3 bytes, but about one token, per rune).
//
// Endpoints get their counts through providers.Response.CountTokens, which
// prefers real usageMetadata from the wire over this heuristic.
//
// Estimates are not billing-grade; they exist so clients that track context
// or cost get a plausible signal instead of zeros.
func EstimateTokens(text string) int {
	var cjk, other int
	for _, r := range text {
		if isCJKRune(r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4
}

// isCJKRune reports whether r belongs to a script that SentencePiece-style
// tokenizers emit at roughly one token per character.
func isCJKRune(r rune) bool {
	if r < 0x1100 { // below Hangul Jamo, the first block of these scripts
		return false
	}
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// BuildPromptFromMessages constructs a unified prompt from messages
func BuildPromptFromMessages(messages []models.Message, systemPrompt string) string {
	var promptBuilder strings.Builder

	if systemPrompt != "" {
		promptBuilder.WriteString(fmt.Sprintf("System: %s\n\n", systemPrompt))
	}

	for _, msg := range messages {
		role := "User"
		if strings.EqualFold(msg.Role, "assistant") || strings.EqualFold(msg.Role, "model") {
			role = "Model"
		} else if strings.EqualFold(msg.Role, "system") {
			role = "System"
		}
		content := msg.Content
		if strings.TrimSpace(content) == "" && len(msg.Attachments) > 0 {
			content = fmt.Sprintf("[%d file(s) attached]", len(msg.Attachments))
		}
		promptBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, content))
	}

	return strings.TrimSpace(promptBuilder.String())
}

// ValidateMessages validates that messages array is not empty and not all empty
func ValidateMessages(messages []models.Message) error {
	if len(messages) == 0 {
		return fmt.Errorf("messages array cannot be empty")
	}

	allEmpty := true
	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) != "" || len(msg.Attachments) > 0 {
			allEmpty = false
			break
		}
	}

	if allEmpty {
		return fmt.Errorf("all messages have empty content")
	}

	return nil
}

// ValidateGenerationRequest validates common generation request parameters
func ValidateGenerationRequest(model string, maxTokens int, temperature float32) error {
	if maxTokens < 0 {
		return fmt.Errorf("max_tokens must be non-negative")
	}

	if temperature < 0 || temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2")
	}

	return nil
}

// MarshalJSONSafely marshals JSON and logs errors instead of silently failing
func MarshalJSONSafely(log *zap.Logger, v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		log.Error("Failed to marshal JSON", zap.Error(err), zap.Any("value", v))
		return []byte("{}")
	}
	return data
}

// SendStreamChunk writes a JSON chunk to the stream writer with error handling
func SendStreamChunk(w *bufio.Writer, log *zap.Logger, chunk interface{}) error {
	data := MarshalJSONSafely(log, chunk)
	if _, err := w.Write(data); err != nil {
		log.Error("Failed to write chunk", zap.Error(err))
		return err
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		log.Error("Failed to write newline", zap.Error(err))
		return err
	}
	if err := w.Flush(); err != nil {
		log.Error("Failed to flush writer", zap.Error(err))
		return err
	}
	return nil
}

// SendSSEChunk writes a Server-Sent Event chunk
func SendSSEChunk(w *bufio.Writer, log *zap.Logger, event string, chunk interface{}) error {
	data := MarshalJSONSafely(log, chunk)
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(data)); err != nil {
		log.Error("Failed to write SSE chunk", zap.Error(err))
		return err
	}
	if err := w.Flush(); err != nil {
		log.Error("Failed to flush SSE writer", zap.Error(err))
		return err
	}
	return nil
}

// SendSSEEvent writes a generic SSE data event by marshaling v as JSON.
// It returns false if writing fails (to signal the caller to stop streaming).
func SendSSEEvent(w *bufio.Writer, log *zap.Logger, v interface{}) bool {
	data := MarshalJSONSafely(log, v)
	if _, err := fmt.Fprintf(w, "data: %s\n\n", string(data)); err != nil {
		log.Error("Failed to write SSE event", zap.Error(err))
		return false
	}
	if err := w.Flush(); err != nil {
		log.Error("Failed to flush SSE event writer", zap.Error(err))
		return false
	}
	return true
}

// SplitResponseIntoChunks simulates streaming by splitting response into chunks
// while preserving all whitespace, newlines, and formatting (strings.Join(chunks, "") == text).
func SplitResponseIntoChunks(text string, delayMs int) []string {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	var chunks []string
	start := 0
	targetChunkSize := 8

	for start < len(runes) {
		end := start + targetChunkSize
		if end >= len(runes) {
			chunks = append(chunks, string(runes[start:]))
			break
		}
		// Try to find a whitespace boundary near target size
		boundary := -1
		for i := end; i < len(runes) && i <= end+10; i++ {
			if runes[i] == ' ' || runes[i] == '\n' || runes[i] == '\t' {
				boundary = i + 1
				break
			}
		}
		if boundary == -1 {
			for i := end; i > start; i-- {
				if runes[i] == ' ' || runes[i] == '\n' || runes[i] == '\t' {
					boundary = i + 1
					break
				}
			}
		}
		if boundary <= start || boundary > len(runes) {
			boundary = end
		}
		chunks = append(chunks, string(runes[start:boundary]))
		start = boundary
	}
	return chunks
}

// ExtractFirstJSONObject finds and extracts the first balanced top-level JSON object from text.
func ExtractFirstJSONObject(text string) string {
	start := strings.Index(text, "{")
	if start < 0 {
		return ""
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}

	return ""
}

// EscapeJSONStringControlChars escapes raw control characters (such as
// newlines) that appear inside JSON string literals. LLMs often write
// multi-line values without escaping them, which encoding/json rejects.
func EscapeJSONStringControlChars(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	inString := false
	escaped := false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if !inString {
			if ch == '"' {
				inString = true
			}
			b.WriteByte(ch)
			continue
		}
		if escaped {
			escaped = false
			b.WriteByte(ch)
			continue
		}
		switch {
		case ch == '\\':
			escaped = true
			b.WriteByte(ch)
		case ch == '"':
			inString = false
			b.WriteByte(ch)
		case ch == '\n':
			b.WriteString(`\n`)
		case ch == '\r':
			b.WriteString(`\r`)
		case ch == '\t':
			b.WriteString(`\t`)
		case ch < 0x20:
			fmt.Fprintf(&b, `\u%04x`, ch)
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// SleepWithCancel sleeps for the specified duration or until context is cancelled
func SleepWithCancel(ctx context.Context, duration time.Duration) bool {
	select {
	case <-time.After(duration):
		return true
	case <-ctx.Done():
		return false
	}
}

// ErrorToResponse converts an error to a standardized error response
func ErrorToResponse(err error, errorType string) models.ErrorResponse {
	return models.ErrorResponse{
		Error: models.Error{
			Message: err.Error(),
			Type:    errorType,
		},
	}
}

// StripCodeFence removes markdown code fences from a string, supporting json and JSON labels.
func StripCodeFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimPrefix(trimmed, "json")
	trimmed = strings.TrimPrefix(trimmed, "JSON")
	trimmed = strings.TrimSpace(trimmed)
	if idx := strings.LastIndex(trimmed, "```"); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	return trimmed
}

// StripCitationArtifacts removes the citation and code-event artifacts that Gemini's
// web UI injects into response text. It is deliberately narrow: only "[cite: N]" /
// "[cite: N, M]" lists, "[cite_start]" markers and "?code_reference..." /
// "?code_stdout..." suffixes on markdown code-fence opening lines are removed, so
// ordinary prose (including literal brackets and unfenced query strings) is left
// untouched.
func StripCitationArtifacts(text string) string {
	if text == "" {
		return text
	}
	if !strings.Contains(text, "[cite") && !strings.Contains(text, "?code_") {
		return text
	}

	text = removeInlineMarkers(text, citationMarkerRe)
	text = removeInlineMarkers(text, citeStartMarkerRe)

	if !strings.Contains(text, "?code_") {
		return text
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		m := codeFenceArtifactRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if strings.HasSuffix(line, "\r") {
			lines[i] = m[1] + "\r"
			continue
		}
		lines[i] = m[1]
	}
	return strings.Join(lines, "\n")
}

// removeInlineMarkers deletes every match of re from text. When a marker sits
// between words it is simply cut out; when what follows it closes the phrase
// (see closesPhrase), one preceding space is removed too so no "word ." or
// trailing space is left behind.
func removeInlineMarkers(text string, re *regexp.Regexp) string {
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}

	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if start > last && text[start-1] == ' ' && closesPhrase(text[end:]) {
			start--
		}
		b.WriteString(text[last:start])
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

// closingPunctuation lists sentence and clause punctuation, ASCII and CJK,
// that attaches to the word before it.
const closingPunctuation = ".,;:!?…。、，．；：！？"

// closesPhrase reports whether rest, the text right after a removed marker,
// attaches to the word before the marker: the end of the text, whitespace,
// closing punctuation, or a closing run of markdown delimiters or quotes as in
// "**claim [cite: 1]**.". Anything else starts a new phrase whose leading space
// must stay, such as the bold label in "* [cite_start]**Label:** text".
func closesPhrase(rest string) bool {
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	switch {
	case unicode.IsSpace(r), strings.ContainsRune(closingPunctuation, r):
		return true
	case unicode.Is(unicode.Pe, r), unicode.Is(unicode.Pf, r): // ) ] } 」 ” ...
		return true
	case strings.ContainsRune("*_~`\"'", r):
		// A delimiter run closes when no word follows it.
		return closesPhrase(strings.TrimLeft(rest, string(r)))
	}
	return false
}

// ExtractThinkingAndText separates the thinking process and final text from the raw string.
// It searches for the start token (e.g. <ctrl94>thought) and end token (e.g. <ctrl95>).
func ExtractThinkingAndText(raw string) (string, string) {
	var startIdx = -1
	var startLen = 0

	markers := []string{"<ctrl94>thought", "<ctrl94>"}
	for _, marker := range markers {
		if idx := strings.Index(raw, marker); idx != -1 {
			startIdx = idx
			startLen = len(marker)
			break
		}
	}

	if startIdx == -1 {
		return "", raw
	}

	var endIdx = -1
	var endLen = 0
	endMarkers := []string{"<ctrl95>"}
	for _, marker := range endMarkers {
		if idx := strings.Index(raw[startIdx+startLen:], marker); idx != -1 {
			endIdx = startIdx + startLen + idx
			endLen = len(marker)
			break
		}
	}

	if endIdx == -1 {
		thinking := strings.TrimSpace(raw[startIdx+startLen:])
		textBefore := strings.TrimSpace(raw[:startIdx])
		return thinking, textBefore
	}

	thinking := strings.TrimSpace(raw[startIdx+startLen : endIdx])
	textBefore := raw[:startIdx]
	textAfter := raw[endIdx+endLen:]

	textContent := strings.TrimSpace(textBefore + textAfter)
	return thinking, textContent
}
