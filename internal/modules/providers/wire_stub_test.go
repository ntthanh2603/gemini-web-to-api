package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/imroc/req/v3"
)

// wireStub records every Gemini Web request the client makes and answers it,
// without ever forwarding to the network. It speaks the resumable upload
// protocol and the StreamGenerate wire format, and is installed on BOTH HTTP
// paths the client uses: the req client's transport (uploads) and
// http.DefaultTransport (StreamGenerate + image download).
type wireStub struct {
	mu             sync.Mutex
	seen           []string // "METHOD host/path", in order
	uploadedNames  []string
	uploadedBytes  [][]byte
	attached       []string // "file-id name" as sent in the generation request
	promptSent     string
	generatedURL   string // URL the stubbed generation answer advertises
	downloadBytes  []byte // bytes served for the generated image
	allowDownloads bool
}

func newWireStub(t *testing.T, generatedURL string, downloadBytes []byte) (*wireStub, *req.Client) {
	t.Helper()
	s := &wireStub{generatedURL: generatedURL, downloadBytes: downloadBytes, allowDownloads: downloadBytes != nil}
	handler := func(r *http.Request) (*http.Response, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Host+r.URL.Path)
		switch {
		case r.Method == http.MethodOptions:
			return wireText(http.StatusOK, ""), nil
		case r.Method == http.MethodPost && r.URL.Path == "/upload":
			body, _ := io.ReadAll(r.Body)
			s.uploadedNames = append(s.uploadedNames, strings.TrimPrefix(string(body), "File name: "))
			resp := wireText(http.StatusOK, "")
			resp.Header.Set("X-Goog-Upload-Url", "https://content-push.googleapis.com/session")
			return resp, nil
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			body, _ := io.ReadAll(r.Body)
			s.uploadedBytes = append(s.uploadedBytes, body)
			return wireText(http.StatusOK, "file-id-"+string(rune('0'+len(s.uploadedBytes)))), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "StreamGenerate"):
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			var outer []interface{}
			if err := json.Unmarshal([]byte(r.PostForm.Get("f.req")), &outer); err != nil || len(outer) != 2 {
				t.Fatalf("invalid outer payload: %v", err)
			}
			innerRaw, _ := outer[1].(string)
			var inner []interface{}
			if err := json.Unmarshal([]byte(innerRaw), &inner); err != nil || len(inner) != 81 {
				t.Fatalf("invalid generation payload: len=%d err=%v", len(inner), err)
			}
			msg, _ := inner[0].([]interface{})
			s.promptSent, _ = msg[0].(string)
			if fd, ok := msg[3].([]interface{}); ok {
				for _, raw := range fd {
					pair, _ := raw.([]interface{})
					if len(pair) < 2 {
						continue
					}
					id, _ := pair[0].([]interface{})
					name, _ := pair[1].(string)
					idStr := ""
					if len(id) > 0 {
						idStr, _ = id[0].(string)
					}
					s.attached = append(s.attached, idStr+" "+name)
				}
			}
			return wireText(http.StatusOK, wireGenerationBody(s.generatedURL)), nil
		case r.Method == http.MethodGet && s.allowDownloads && strings.Contains(r.URL.Path, "/gg-dl/"):
			resp := wireText(http.StatusOK, string(s.downloadBytes))
			resp.Header.Set("Content-Type", "image/png")
			return resp, nil
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		return wireText(http.StatusBadRequest, ""), nil
	}

	httpClient := req.NewClient()
	httpClient.GetTransport().WrapRoundTripFunc(func(http.RoundTripper) req.HttpRoundTripFunc { return handler })
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(handler)
	return s, httpClient
}

// wireGenerationBody builds a StreamGenerate answer carrying one generated image
// in candidate[12][7][0], the slot extractGeneratedImages reads.
func wireGenerationBody(url string) string {
	meta := []interface{}{nil, float64(1), "generated.png", url, nil, "image/png", []interface{}{float64(1024), float64(1024), float64(1)}}
	imageEntry := []interface{}{[]interface{}{nil, nil, nil, meta}, []interface{}{"0_1", 0}}
	slot := make([]interface{}, 8)
	slot[7] = []interface{}{[]interface{}{imageEntry}}
	candidate := make([]interface{}, 13)
	candidate[12] = slot
	candidate[0] = "rc_1"
	candidate[1] = []interface{}{"Done."}
	payload, _ := json.Marshal([]interface{}{nil, []interface{}{"c_1", "r_1"}, nil, nil, []interface{}{candidate}})
	body, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", nil, string(payload)}})
	return string(body)
}

func wireText(status int, body string) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "text/plain")
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func wireTestModels() []geminiModel {
	return []geminiModel{
		{ModelInfo: ModelInfo{ID: "gemini-pro"}, ModelID: "2222222222222222", Capacity: 2, CapacityField: 12, ModelNumber: 3},
	}
}
