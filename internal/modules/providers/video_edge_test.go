package providers

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func mp4Box(kind string, payload []byte) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:], kind)
	return append(b, payload...)
}
func testMP4() []byte {
	b := mp4Box("ftyp", []byte("isom0000"))
	b = append(b, mp4Box("moov", []byte{1})...)
	return append(b, mp4Box("mdat", []byte{2, 3})...)
}

type shortVideoWriter struct{}

func (shortVideoWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }
func TestMP4ContainerEdgeCases(t *testing.T) {
	good := testMP4()
	cases := []struct {
		name  string
		b     []byte
		limit int64
		ok    bool
	}{
		{"valid", good, 1024, true}, {"truncated header", good[:5], 1024, false}, {"signature only", good[:12], 1024, false}, {"truncated payload", good[:len(good)-1], 1024, false},
		{"missing metadata", append(mp4Box("ftyp", []byte("isom0000")), mp4Box("mdat", []byte{1})...), 1024, false},
		{"image brand", bytes.Replace(good, []byte("isom"), []byte("avif"), 1), 1024, false},
		{"size bound", good, 20, false},
		{"trailing garbage", append(append([]byte{}, good...), 1), 1024, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dst bytes.Buffer
			n, err := copyMP4(&dst, bytes.NewReader(tc.b), tc.limit)
			if (err == nil) != tc.ok {
				t.Fatalf("n=%d err=%v", n, err)
			}
			if tc.ok && !bytes.Equal(dst.Bytes(), tc.b) {
				t.Fatal("bytes changed")
			}
		})
	}
	if _, err := copyMP4(shortVideoWriter{}, bytes.NewReader(good), 1024); err == nil {
		t.Fatal("short write accepted")
	}
	extended := make([]byte, 16)
	binary.BigEndian.PutUint32(extended, 1)
	copy(extended[4:], "free")
	binary.BigEndian.PutUint64(extended[8:], 16)
	b := append(append([]byte{}, good...), extended...)
	if _, err := copyMP4(io.Discard, bytes.NewReader(b), 1024); err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint64(extended[8:], ^uint64(0))
	b = append(append([]byte{}, good...), extended...)
	if _, err := copyMP4(io.Discard, bytes.NewReader(b), 1024); err == nil {
		t.Fatal("overflow size accepted")
	}
}
func TestVideoHistoryMultipleFramesAndRPCRejection(t *testing.T) {
	candidate := videoFixture(true, "https://lh3.googleusercontent.com/video")
	empty := videoFrame("hNvQHb", []any{[]any{}})
	full := videoFrame("hNvQHb", []any{[]any{[]any{nil, nil, nil, []any{[]any{candidate}}}}})
	combined := append(empty, '\n')
	combined = append(combined, bytes.TrimPrefix(full, []byte(")]}'"))...)
	if video, _, err := parseVideoHistory(combined); err != nil || video == nil {
		t.Fatalf("lost later video: %v", err)
	}
	for _, code := range []int{7, 16, 8} {
		b, _ := json.Marshal([]any{[]any{"wrb.fr", "hNvQHb", nil, nil, nil, []int{code}}})
		_, _, err := parseVideoHistory(b)
		var ve *VideoError
		if !errors.As(err, &ve) || ve.Code == "poll_failed" {
			t.Fatalf("code %d: %v", code, err)
		}
	}
	if _, _, err := parseVideoHistory(videoFrame("hNvQHb", map[string]any{"unexpected": "object"})); err == nil {
		t.Fatal("malformed response called pending")
	}
}
func TestVideoRefusalWithoutConversationAndRedaction(t *testing.T) {
	body := videoFrame("generate", []any{nil, nil, nil, nil, []any{[]any{"rc_test", []any{"I cannot generate this video."}}}})
	r, err := parseVideoGeneration(body)
	if err != nil || r.Text == "" {
		t.Fatalf("lost explanation: %v", err)
	}
	message := safeVideoMessage("Urdu شاعری HTTPS://google.com/video?token=secret //google.com/private " + strings.Repeat("ب", 2000))
	if strings.Contains(message, "secret") || strings.Contains(message, "private") || len([]rune(message)) > 1000 {
		t.Fatal("unsafe message")
	}
	if videoField([]any{1}, -1) != nil {
		t.Fatal("negative field")
	}
}
func TestVideoDownloadHTTPFailures(t *testing.T) {
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	c := &Client{}
	for _, status := range []int{401, 403, 404, 429, 500} {
		http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private body")), Request: r}, nil
		})
		err := c.DownloadVideo(context.Background(), Video{URL: "https://googleusercontent.com/video"}, io.Discard)
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal(err)
		}
	}
	for _, length := range []int64{(100 << 20) + 1, 200} {
		http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ContentLength: length, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(testMP4())), Request: r}, nil
		})
		if err := c.DownloadVideo(context.Background(), Video{URL: "https://googleusercontent.com/video"}, io.Discard); err == nil {
			t.Fatal("invalid length accepted")
		}
	}
}
func TestRegularGenerationStillRetriesAndKeepsTemporaryMode(t *testing.T) {
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	calls := 0
	http.DefaultTransport = videoTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		r.ParseForm()
		var outer []any
		json.Unmarshal([]byte(r.Form.Get("f.req")), &outer)
		var inner []any
		json.Unmarshal([]byte(outer[1].(string)), &inner)
		if inner[45] != float64(1) {
			t.Error("ordinary temporary chat changed")
		}
		status := 503
		body := []byte("retry")
		if calls == 2 {
			status = 200
			body = videoFrame("generate", []any{nil, "c_test", nil, nil, []any{[]any{"rc_test", []any{"ordinary text"}}}})
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})
	c := &Client{at: "token", cookieHeader: "cookie", maxRetries: 2, defaultTemporary: true, log: zap.NewNop(), cachedModels: []geminiModel{{ModelInfo: ModelInfo{ID: "gemini-pro"}, ModelID: "pro", ModelNumber: 3, Capacity: 2, CapacityField: 12}}}
	result, err := c.GenerateContent(context.Background(), "hello", WithModel("gemini-pro"))
	if err != nil || result.Text != "ordinary text" || calls != 2 {
		t.Fatalf("text regression: %v calls=%d", err, calls)
	}
}
func FuzzVideoParsers(f *testing.F) {
	f.Add([]byte(""))
	f.Add(videoFrame("hNvQHb", []any{[]any{}}))
	f.Add(videoFrame("generate", []any{nil, []any{"c_test", "r_test"}}))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			t.Skip()
		}
		parseVideoGeneration(b)
		parseVideoHistory(b)
	})
}
func FuzzMP4Reader(f *testing.F) {
	f.Add(testMP4())
	f.Add([]byte("not a video"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			t.Skip()
		}
		n, err := copyMP4(io.Discard, bytes.NewReader(b), 1<<20)
		if err == nil && n != int64(len(b)) {
			t.Fatal("accepted incomplete input")
		}
	})
}
