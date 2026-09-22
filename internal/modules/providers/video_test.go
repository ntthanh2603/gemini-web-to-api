package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func videoFixture(sparse bool, url string) []any {
	card := []any{[]any{nil, nil, nil, nil, nil, nil, nil, []any{"https://lh3.googleusercontent.com/thumb", url}}}
	field := []any{[]any{[]any{card}}}
	rich := make([]any, 60)
	rich[59] = field
	if sparse {
		rich = []any{map[string]any{"60": field}}
	}
	candidate := make([]any, 13)
	candidate[0] = "rc_test"
	candidate[1] = []any{"Video ready"}
	candidate[8] = []any{2}
	candidate[12] = rich
	return candidate
}
func videoFrame(rpc string, payload any) []byte {
	p, _ := json.Marshal(payload)
	b, _ := json.Marshal([]any{[]any{"wrb.fr", rpc, string(p)}})
	return append([]byte(")]}'\n123\n"), b...)
}
func TestVideoProtocol(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		c := videoFixture(sparse, "https://lh3.googleusercontent.com/video")
		payload := []any{nil, []any{"c_test", "r_test"}, nil, nil, []any{c}}
		r, err := parseVideoGeneration(videoFrame("generate", payload))
		if err != nil || len(r.Videos) != 1 || r.ConversationID != "c_test" {
			t.Fatalf("sparse=%v: %+v %v", sparse, r, err)
		}
		history := []any{[]any{[]any{[]any{"c_test", "r_test"}, nil, nil, []any{[]any{c}}}}}
		v, done, err := parseVideoHistory(videoFrame("hNvQHb", history))
		if err != nil || !done || v == nil {
			t.Fatalf("history: %v %v %v", v, done, err)
		}
	}
}
func TestVideoRejectsUntrustedAndMalformed(t *testing.T) {
	for _, raw := range []string{"http://google.com/video", "https://google.com.attacker.test/x", "https://googleusercontent.com:8080/x", "https://user@google.com/x", "file:///video"} {
		if len(candidateVideos(videoFixture(false, raw))) != 0 {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{"", "not json", `[["wrb.fr","x","null"]]`} {
		if _, err := parseVideoGeneration([]byte(raw)); err == nil {
			t.Fatal("accepted invalid payload")
		}
	}
	if len(candidateVideos([]any{"https://lh3.googleusercontent.com/video"})) != 0 {
		t.Fatal("accepted unrelated URL")
	}
}
func TestVideoHistoryPendingAndNoVideo(t *testing.T) {
	c := make([]any, 13)
	c[8] = []any{1}
	rich := make([]any, 7)
	rich[6] = []any{"working"}
	c[12] = rich
	history := func() []byte { return videoFrame("hNvQHb", []any{[]any{[]any{nil, nil, nil, []any{[]any{c}}}}}) }
	if _, done, err := parseVideoHistory(history()); err != nil || done {
		t.Fatalf("pending %v %v", done, err)
	}
	c[8] = []any{2}
	if _, done, err := parseVideoHistory(history()); err == nil || !done {
		t.Fatalf("finished %v %v", done, err)
	}
}

type videoTransport func(*http.Request) (*http.Response, error)

func (f videoTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestVideoDownloadValidatesContentAndRedirects(t *testing.T) {
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	client := &Client{cookieHeader: "session=private"}
	calls := 0
	http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Cookie") != "session=private" {
			t.Error("missing auth")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(testMP4())), Request: r}, nil
	})
	var dst bytes.Buffer
	if err := client.DownloadVideo(context.Background(), Video{URL: "https://lh3.googleusercontent.com/video"}, &dst); err != nil || dst.Len() != len(testMP4()) {
		t.Fatalf("download: %v, %d", err, dst.Len())
	}
	http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.test/video"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	before := calls
	if err := client.DownloadVideo(context.Background(), Video{URL: "https://lh3.googleusercontent.com/video"}, io.Discard); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls != before+1 {
		t.Fatal("sent request to untrusted host")
	}
	http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("<html>sign in</html>")), Request: r}, nil
	})
	if err := client.DownloadVideo(context.Background(), Video{URL: "https://lh3.googleusercontent.com/video"}, io.Discard); err == nil {
		t.Fatal("accepted HTML")
	}
}
func TestVideoSubmissionIsNeverRetried(t *testing.T) {
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	calls := 0
	http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret upstream data")), Request: r}, nil
	})
	c := &Client{at: "token", cookieHeader: "cookie", maxRetries: 5, log: zap.NewNop(), cachedModels: []geminiModel{{ModelInfo: ModelInfo{ID: "gemini-pro"}, ModelID: "pro", ModelNumber: 3, Capacity: 2, CapacityField: 12}}}
	_, err := c.GenerateVideo(context.Background(), "test", "gemini-pro")
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("leaked upstream response")
	}
}
func TestVideoPartialStreamRetainsConversation(t *testing.T) {
	frame := videoFrame("generate", []any{nil, []any{"c_test", "r_test"}})
	frame = append(frame, []byte("\n125\n[[\"wrb.fr\"")...)
	r, err := parseVideoGeneration(frame)
	if err != nil || r.ConversationID != "c_test" {
		t.Fatalf("lost recovery metadata: %v %v", r, err)
	}
}

func TestVideoHistoryRequestUsesSessionAndAccount(t *testing.T) {
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("rpcids") != "hNvQHb" || !strings.Contains(r.URL.Path, "/u/2/") {
			t.Fatal("wrong history route")
		}
		if r.Header.Get(geminiModelHeaderKey) == "" || r.Header.Get("Cookie") != "session=private" {
			t.Fatal("missing session headers")
		}
		r.ParseForm()
		var envelope []any
		if json.Unmarshal([]byte(r.Form.Get("f.req")), &envelope) != nil {
			t.Fatal("invalid RPC")
		}
		c := videoFixture(true, "https://lh3.googleusercontent.com/video")
		b := videoFrame("hNvQHb", []any{[]any{[]any{nil, nil, nil, []any{[]any{c}}}}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
	})
	c := &Client{at: "token", cookieHeader: "session=private", authUser: "2"}
	video, done, err := c.readVideo(context.Background(), "c_test")
	if err != nil || !done || video == nil {
		t.Fatalf("%v %v %v", video, done, err)
	}
}
func TestMP4DoesNotAcceptImageBrands(t *testing.T) {
	if mp4Brand("avif") || mp4Brand("heic") || !mp4Brand("isom") {
		t.Fatal("invalid brand validation")
	}
}

func TestVideoPollingSurvivesThreeUnrecognizedReplies(t *testing.T) {
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	calls := 0
	http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.Contains(r.URL.Path, "batchexecute") {
			t.Fatal("must never regenerate")
		}
		body := []byte("unrecognized")
		if calls == 4 {
			c := videoFixture(true, "https://lh3.googleusercontent.com/video")
			body = videoFrame("hNvQHb", []any{[]any{[]any{nil, nil, nil, []any{[]any{c}}}}})
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})
	c := &Client{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	video, err := c.waitVideo(ctx, &Response{ConversationID: "c_test"}, time.Millisecond)
	if err != nil || video == nil || calls != 4 {
		t.Fatalf("video=%v err=%v calls=%d", video, err, calls)
	}
}
func TestVideoPollingDeadlineKeepsExplanation(t *testing.T) {
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("bad response")), Request: r}, nil
	})
	c := &Client{}
	_, err := c.waitVideo(ctx, &Response{ConversationID: "c_test", Text: "Generating https://google.com/private?token=secret"}, time.Millisecond)
	var ve *VideoError
	if !errors.As(err, &ve) || ve.Code != "poll_failed" || strings.Contains(ve.ProviderMessage, "secret") {
		t.Fatalf("unsafe or missing diagnosis: %v", err)
	}
}
func TestVideoRefusalDoesNotPoll(t *testing.T) {
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	calls := 0
	http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.Contains(r.URL.Path, "StreamGenerate") {
			t.Fatal("polled declined generation")
		}
		candidate := []any{"rc_test", []any{"You've reached your video limit. Try tomorrow."}}
		body := videoFrame("generate", []any{nil, []any{"c_test", "r_test"}, nil, nil, []any{candidate}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})
	c := &Client{at: "token", cookieHeader: "cookie", maxRetries: 5, log: zap.NewNop(), cachedModels: []geminiModel{{ModelInfo: ModelInfo{ID: "gemini-pro"}, ModelID: "pro", ModelNumber: 3, Capacity: 2, CapacityField: 12}}}
	var progress VideoProgress
	_, err := c.GenerateVideo(context.Background(), "prompt", "gemini-pro", WithVideoProgress(func(p VideoProgress) { progress = p }))
	var ve *VideoError
	if !errors.As(err, &ve) || ve.Code != "rate_limited" || calls != 1 || progress.ConversationID != "c_test" || ve.ProviderMessage == "" {
		t.Fatalf("err=%v calls=%d progress=%v", err, calls, progress)
	}
}
