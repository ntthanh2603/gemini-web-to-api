package providers

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const (
	frameStart  = `[["wrb.fr",null,"[null,[\"c_1\",\"r_1\"],{\"18\":\"r_1\",\"44\":true}]"]]`
	frameAnswer = `[["wrb.fr",null,"[null,[\"c_1\",\"r_1\"],null,null,[[\"rc_1\",[\"OK\"]]]]"]]`
	frameEnd    = `[["e",10,null,null,123]]`
	frameBard   = `[["wrb.fr",null,null,null,null,[13,null,[["type.googleapis.com/assistant.boq.bard.application.BardErrorInfo",[1100]]]]]]`
)

// pipeBody returns a body whose writes the test controls.
func pipeBody() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }

func writeLines(w *io.PipeWriter, lines ...string) {
	for _, l := range lines {
		_, _ = w.Write([]byte(l + "\n"))
	}
}

func TestReadGenerateStreamReturnsOnEndFrameWithoutEOF(t *testing.T) {
	r, w := pipeBody()
	go writeLines(w, ")]}'", "", "10", frameStart, "20", frameAnswer, "5", frameEnd)
	// w is never closed: the reader must not wait for EOF.

	start := time.Now()
	data, err := readGenerateStream(context.Background(), r, 2*time.Second, 2*time.Second, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("reader waited for EOF instead of stopping at the end frame")
	}
	resp, err := (&Client{}).parseResponse(string(data))
	if err != nil || resp.Text != "OK" {
		t.Fatalf("parse = %v, %v; want text OK", resp, err)
	}
}

func TestReadGenerateStreamStallsBeforeAnswer(t *testing.T) {
	r, w := pipeBody()
	go writeLines(w, ")]}'", "10", frameStart)
	defer w.Close()

	_, err := readGenerateStream(context.Background(), r, 150*time.Millisecond, time.Second, nil)
	if !errors.Is(err, errStreamStalled) {
		t.Fatalf("err = %v, want errStreamStalled", err)
	}
	if !strings.Contains(err.Error(), "before the answer started") {
		t.Fatalf("err = %v, want phase 'before the answer started'", err)
	}
}

func TestReadGenerateStreamStallsAfterAnswerUsesIdleTimeout(t *testing.T) {
	r, w := pipeBody()
	go writeLines(w, ")]}'", "20", frameAnswer)
	defer w.Close()

	start := time.Now()
	_, err := readGenerateStream(context.Background(), r, 5*time.Second, 150*time.Millisecond, nil)
	if !errors.Is(err, errStreamStalled) {
		t.Fatalf("err = %v, want errStreamStalled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("idle timeout not applied after the answer started")
	}
}

func TestReadGenerateStreamFailsFastOnBardError(t *testing.T) {
	r, w := pipeBody()
	go writeLines(w, ")]}'", "30", frameBard)
	defer w.Close()

	_, err := readGenerateStream(context.Background(), r, 5*time.Second, 5*time.Second, nil)
	var be *bardError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want *bardError", err)
	}
}

func TestReadGenerateStreamEOFWithoutTrailingNewline(t *testing.T) {
	body := io.NopCloser(strings.NewReader(")]}'\n\n20\n" + frameAnswer + "\n5\n" + frameEnd))
	data, err := readGenerateStream(context.Background(), body, time.Second, time.Second, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(data), frameEnd) {
		t.Fatalf("data truncated: %q", data)
	}
}

func TestReadGenerateStreamDisabledTimeoutsWaitForEOF(t *testing.T) {
	r, w := pipeBody()
	go func() {
		writeLines(w, ")]}'", "10", frameStart)
		time.Sleep(200 * time.Millisecond)
		writeLines(w, "20", frameAnswer)
		_ = w.Close()
	}()
	data, err := readGenerateStream(context.Background(), r, 0, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(data), "rc_1") {
		t.Fatalf("answer frame missing: %q", data)
	}
}

func TestReadGenerateStreamHonoursContext(t *testing.T) {
	r, w := pipeBody()
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := readGenerateStream(ctx, r, 0, 0, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestIsErrorReply(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"I seem to be encountering an error. Can I try something else for you?", true},
		{"  i seem to be encountering an error. can i try something else for you?\n", true},
		{"OK", false},
		{"", false},
		{"Here is why I seem to be encountering an error. Can I try something else for you? in your code", false},
	}
	for _, c := range cases {
		if got := isErrorReply(c.text, defaultErrorReplies); got != c.want {
			t.Errorf("isErrorReply(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	if isErrorReply("OK", nil) {
		t.Error("empty pattern list must never match")
	}
}
