package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// errStreamStalled reports a StreamGenerate response that stopped sending data.
// Gemini Web sometimes leaves the connection open after a failed turn (the web
// UI shows an error and offers "Redo"), so waiting for EOF would hang until the
// HTTP client timeout. The generate loop treats this error as retryable.
var errStreamStalled = errors.New("gemini stream stalled")

// errGeminiErrorReply reports a turn whose whole answer is one of Gemini Web's
// canned failure messages, e.g. "I seem to be encountering an error. Can I try
// something else for you?". It is retryable, exactly like pressing "Redo".
var errGeminiErrorReply = errors.New("gemini replied with a canned error message")

// defaultErrorReplies are canned failure messages Gemini Web returns as an
// ordinary answer. A reply is only treated as an error when its whole text
// matches one of these, so answers that merely mention an error are kept.
var defaultErrorReplies = []string{
	"I seem to be encountering an error. Can I try something else for you?",
	"I encountered an error doing what you asked. Could you try again?",
	// Gemini also uses this wording for genuine refusals; retrying one costs up
	// to GEMINI_MAX_RETRIES extra requests and then surfaces as an error.
	"I'm having a hard time fulfilling your request. Can I help you with something else instead?",
	"Something went wrong. Please try again.",
	"Sorry, something went wrong. Please try your request again.",
	"Something went wrong.",
}

// streamSignals is what the stream reader learns from the frames seen so far.
type streamSignals struct {
	finished  bool   // the ["e", ...] end-of-response frame arrived
	sawAnswer bool   // a frame carried answer content
	bardErr   string // a BardErrorInfo frame arrived
}

// scanFrames inspects the complete lines of data starting at offset and returns
// the offset after the last complete line. Incomplete trailing lines are left
// for the next call.
func scanFrames(data []byte, offset int, s *streamSignals) int {
	for {
		nl := bytes.IndexByte(data[offset:], '\n')
		if nl < 0 {
			return offset
		}
		inspectFrameLine(data[offset:offset+nl], s)
		offset += nl + 1
	}
}

func inspectFrameLine(raw []byte, s *streamSignals) {
	line := strings.TrimPrefix(strings.TrimSpace(string(raw)), ")]}'")
	if !strings.HasPrefix(line, "[") {
		return
	}
	var root []interface{}
	if err := json.Unmarshal([]byte(line), &root); err != nil {
		return
	}
	for _, item := range root {
		arr, ok := item.([]interface{})
		if !ok || len(arr) == 0 {
			continue
		}
		if tag, _ := arr[0].(string); tag == "e" {
			s.finished = true
			continue
		}
		if errStr := extractBardError(arr); errStr != "" {
			s.bardErr = errStr
			continue
		}
		if len(arr) < 3 {
			continue
		}
		payloadStr, ok := arr[2].(string)
		if !ok {
			continue
		}
		var payload []interface{}
		if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
			continue
		}
		if len(payload) > 4 {
			if candidates, ok := payload[4].([]interface{}); ok && len(candidates) > 0 {
				s.sawAnswer = true
			}
		}
		if len(payload) > 2 {
			if fields, ok := payload[2].(map[string]interface{}); ok {
				if _, ok := fields["7"]; ok {
					s.sawAnswer = true
				}
			}
		}
	}
}

// readGenerateStream reads a StreamGenerate response incrementally. It returns
// as soon as the end-of-response frame arrives, fails fast on a BardErrorInfo
// frame, and gives up with errStreamStalled when no answer arrives within
// firstResponse or the stream goes quiet for idle after the answer started.
// A zero duration disables that timeout.
func readGenerateStream(ctx context.Context, body io.ReadCloser, firstResponse, idle time.Duration, onChunk func(elapsed time.Duration, chunk []byte)) ([]byte, error) {
	type readResult struct {
		chunk []byte
		err   error
	}
	results := make(chan readResult)
	done := make(chan struct{})
	defer func() {
		close(done)
		_ = body.Close()
	}()

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := body.Read(buf)
			var chunk []byte
			if n > 0 {
				chunk = append([]byte(nil), buf[:n]...)
			}
			select {
			case results <- readResult{chunk: chunk, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	start := time.Now()
	var data []byte
	var signals streamSignals
	offset := 0
	for {
		timeout := firstResponse
		if signals.sawAnswer {
			timeout = idle
		}
		var timer <-chan time.Time
		if timeout > 0 {
			// Unreferenced timers are garbage collected (Go 1.23+), so no Stop.
			timer = time.NewTimer(timeout).C
		}

		select {
		case r := <-results:
			if len(r.chunk) > 0 {
				data = append(data, r.chunk...)
				if onChunk != nil {
					onChunk(time.Since(start), r.chunk)
				}
				offset = scanFrames(data, offset, &signals)
				if signals.bardErr != "" {
					return data, &bardError{msg: signals.bardErr}
				}
				if signals.finished {
					return data, nil
				}
			}
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					// The last frame may lack a trailing newline.
					inspectFrameLine(data[offset:], &signals)
					if signals.bardErr != "" {
						return data, &bardError{msg: signals.bardErr}
					}
					return data, nil
				}
				return data, fmt.Errorf("failed to read generate response: %w", r.err)
			}
		case <-timer:
			phase := "before the answer started"
			if signals.sawAnswer {
				phase = "after the answer started"
			}
			return data, fmt.Errorf("%w: no data for %s %s", errStreamStalled, timeout, phase)
		case <-ctx.Done():
			return data, ctx.Err()
		}
	}
}

// isErrorReply reports whether text is exactly one of the canned failure
// messages, ignoring case and surrounding whitespace.
func isErrorReply(text string, replies []string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, r := range replies {
		if r = strings.TrimSpace(r); r != "" && strings.EqualFold(text, r) {
			return true
		}
	}
	return false
}

// streamDebugLogger logs every received chunk when GEMINI_DEBUG_STREAM=1, which
// helps capture new Gemini Web failure shapes.
func (c *Client) streamDebugLogger() func(time.Duration, []byte) {
	if os.Getenv("GEMINI_DEBUG_STREAM") != "1" {
		return nil
	}
	return func(elapsed time.Duration, chunk []byte) {
		s := string(chunk)
		if len(s) > 600 {
			s = s[:300] + " ... " + s[len(s)-300:]
		}
		c.log.Info("Gemini stream chunk",
			zap.Int64("ms", elapsed.Milliseconds()),
			zap.Int("bytes", len(chunk)),
			zap.String("data", s),
		)
	}
}
