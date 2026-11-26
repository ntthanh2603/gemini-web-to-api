package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/imroc/req/v3"
	"go.uber.org/zap"
)

// TestBuildImageEditPromptKeepsInstructionAndMarksReferences pins the prompt
// contract for image edits: the user's instruction must survive verbatim and
// every reference image must be announced in upload order, since that ordering
// is the only thing tying an uploaded file to "the first image" in the text.
func TestBuildImageEditPromptKeepsInstructionAndMarksReferences(t *testing.T) {
	got := buildImageEditPrompt("Make the cat wear a hat", []string{"ref 1.png", "ref 2.png"}, "")
	for _, want := range []string{"Make the cat wear a hat", "Image 1", "Image 2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
	if i1, i2 := strings.Index(got, "Image 1"), strings.Index(got, "Image 2"); i1 > i2 {
		t.Fatalf("references must be announced in upload order:\n%s", got)
	}
}

// TestBuildImageEditPromptAppendsSize ensures an explicit size still reaches the
// model as guidance, matching the text-to-image endpoint's behaviour.
func TestBuildImageEditPromptAppendsSize(t *testing.T) {
	if got := buildImageEditPrompt("edit it", nil, "1536x1024"); !strings.Contains(got, "1536x1024") {
		t.Fatalf("size not passed through:\n%s", got)
	}
}

// TestCreateImageEditUploadsEveryReferenceAndSendsEditInstruction drives the
// real request path and asserts on what actually leaves the process.
//
// The stub is installed on the client's own transport and never forwards to the
// underlying RoundTripper, so this test proves the full wire behaviour with no
// network access: one resumable upload per reference image (bytes intact), then
// a StreamGenerate request that carries the returned file ids in upload order
// together with the edit instruction and its reference markers.
func TestCreateImageEditUploadsEveryReferenceAndSendsEditInstruction(t *testing.T) {
	pngA := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff}
	pngB := []byte{0x89, 0x50, 0x4e, 0x47, 0x11, 0xff}
	generatedURL := "https://lh3.googleusercontent.com/gg-dl/edited-result"

	var uploadedNames, seen []string
	var uploadedBodies [][]byte
	finalizeCount := 0
	generateStartedAt := -1

	// Two HTTP paths are involved and both must be stubbed, otherwise part of
	// the flow silently reaches the real network: uploads go through the req
	// client's transport, the StreamGenerate request through a stdlib client
	// built on http.DefaultTransport.
	stub := func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Method+" "+r.URL.Host+r.URL.Path)
		switch {
		case r.Method == http.MethodOptions:
			return textResponse(http.StatusOK, ""), nil
		case r.Method == http.MethodPost && r.URL.Path == "/upload":
			body, _ := io.ReadAll(r.Body)
			uploadedNames = append(uploadedNames, strings.TrimPrefix(string(body), "File name: "))
			resp := textResponse(http.StatusOK, "")
			// Same host as EndpointUpload, so the URL passes validateUploadURL.
			resp.Header.Set("X-Goog-Upload-Url", "https://content-push.googleapis.com/session")
			return resp, nil
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			body, _ := io.ReadAll(r.Body)
			uploadedBodies = append(uploadedBodies, body)
			finalizeCount++
			return textResponse(http.StatusOK, "file-id-"+string(rune('0'+finalizeCount))), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "StreamGenerate"):
			generateStartedAt = len(seen) - 1
			return generateEditStubResponse(t, r, len(uploadedNames), generatedURL)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/gg-dl/edited-result"):
			// Download of the generated image; answer with real image bytes so
			// B64JSON is populated exactly as in production.
			resp := textResponse(http.StatusOK, string(pngA))
			resp.Header.Set("Content-Type", "image/png")
			return resp, nil
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		return textResponse(http.StatusBadRequest, ""), nil
	}
	httpClient := req.NewClient()
	httpClient.GetTransport().WrapRoundTripFunc(func(http.RoundTripper) req.HttpRoundTripFunc { return stub })
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(stub)

	models := []geminiModel{
		{ModelInfo: ModelInfo{ID: "gemini-pro"}, ModelID: "2222222222222222", Capacity: 2, CapacityField: 12, ModelNumber: 3},
	}
	client := &Client{at: "token", cookieHeader: "SID=x", cachedModels: models, httpClient: httpClient, log: zap.NewNop()}

	resp, err := client.CreateImageEdit(context.Background(), ImageEditRequest{
		Prompt: "Make the cat wear a hat",
		Model:  "gemini-pro",
		Size:   "1024x1024",
		Files: []InputFile{
			{Name: "a.png", MimeType: "image/png", Data: pngA},
			{Name: "b.png", MimeType: "image/png", Data: pngB},
		},
	})

	// --- assert on the requests that left the process ---
	if len(uploadedNames) != 2 || uploadedNames[0] != "a.png" || uploadedNames[1] != "b.png" {
		t.Errorf("references must be uploaded once each, in order; got %v", uploadedNames)
	}
	if len(uploadedBodies) != 2 || string(uploadedBodies[0]) != string(pngA) || string(uploadedBodies[1]) != string(pngB) {
		t.Errorf("uploaded bytes changed in transit: %d bodies", len(uploadedBodies))
	}
	lastUpload := -1
	for i, s := range seen {
		if strings.HasSuffix(s, "/session") {
			lastUpload = i
		}
	}
	if lastUpload == -1 || generateStartedAt == -1 || lastUpload > generateStartedAt {
		t.Errorf("generation must start after every upload finished; requests: %v", seen)
	}

	// --- assert on the result ---
	if err != nil {
		t.Fatalf("CreateImageEdit: %v (requests: %v)", err, seen)
	}
	if resp.Created == 0 || len(resp.Data) != 1 {
		t.Fatalf("unexpected result: %+v", resp)
	}
	if resp.Data[0].URL != generatedURL || !resp.Data[0].Generated {
		t.Errorf("edited image = %+v", resp.Data[0])
	}
	// Callers of an edit endpoint need bytes, not a URL that expires and is
	// only fetchable with the browser session; B64JSON must be filled.
	if resp.Data[0].B64JSON == "" {
		t.Errorf("edited image must carry downloaded bytes in B64JSON")
	}
}

// generateEditStubResponse validates the generation request and answers it the
// way Gemini Web answers an edit: text plus one generated image in the media slot.
func generateEditStubResponse(t *testing.T, r *http.Request, uploadsSoFar int, generatedURL string) (*http.Response, error) {
	t.Helper()
	if uploadsSoFar != 2 {
		t.Errorf("generation sent before both references were uploaded (%d)", uploadsSoFar)
	}
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
	prompt, _ := msg[0].(string)
	for _, want := range []string{"Make the cat wear a hat", "Image 1: a.png", "Image 2: b.png"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q: %q", want, prompt)
		}
	}
	// msg[3] = file_data: [[[id], name], ...] in upload order
	fileData, _ := msg[3].([]interface{})
	if len(fileData) != 2 {
		t.Fatalf("want 2 attached files, got %d (%v)", len(fileData), fileData)
	}
	first, _ := fileData[0].([]interface{})
	idPair, _ := first[0].([]interface{})
	if idPair[0] != "file-id-1" || first[1] != "a.png" {
		t.Errorf("first attachment = %v, want [file-id-1 a.png]", first)
	}
	second, _ := fileData[1].([]interface{})
	idPair2, _ := second[0].([]interface{})
	if idPair2[0] != "file-id-2" || second[1] != "b.png" {
		t.Errorf("second attachment = %v, want [file-id-2 b.png]", second)
	}

	// candidate[12][7][0][i][0][3] = [nil, n, filename, url, ..., mime]
	meta := []interface{}{nil, float64(1), "generated.png", generatedURL, nil, "image/png", []interface{}{float64(1024), float64(1024), float64(1)}}
	imageEntry := []interface{}{[]interface{}{nil, nil, nil, meta}, []interface{}{"0_1", 0}}
	slot := make([]interface{}, 8)
	slot[7] = []interface{}{[]interface{}{imageEntry}}
	candidate := make([]interface{}, 13)
	candidate[12] = slot
	candidate[0] = "rc_edit"
	candidate[1] = []interface{}{"Edited."}
	payload, _ := json.Marshal([]interface{}{nil, []interface{}{"c_edit", "r_edit"}, nil, nil,
		[]interface{}{candidate}})
	body, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", nil, string(payload)}})
	return textResponse(http.StatusOK, string(body)), nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func textResponse(status int, body string) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "text/plain")
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// TestCreateImageEditRejectsEmptyInput guards the two validation rules before
// any network request is made.
func TestCreateImageEditRejectsEmptyInput(t *testing.T) {
	client := &Client{log: zap.NewNop()}
	if _, err := client.CreateImageEdit(context.Background(), ImageEditRequest{Prompt: "", Files: []InputFile{{Name: "a.png"}}}); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Errorf("empty prompt: %v", err)
	}
	if _, err := client.CreateImageEdit(context.Background(), ImageEditRequest{Prompt: "edit"}); err == nil || !strings.Contains(err.Error(), "at least one image") {
		t.Errorf("no images: %v", err)
	}
}
