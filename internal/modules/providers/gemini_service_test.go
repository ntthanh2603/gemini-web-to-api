package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imroc/req/v3"
	"go.uber.org/zap"
)

func TestRefreshSessionHealthKeepsHealthyWhenRotationIsRejected(t *testing.T) {
	rotationErr := errors.New("rotation failed with status 401")
	refreshCalled := false

	gotRotationErr, sessionErr, healthy := refreshSessionHealth(
		func() error { return rotationErr },
		func() error {
			refreshCalled = true
			return nil
		},
	)

	if !refreshCalled {
		t.Fatal("expected Gemini session verification after rotation failure")
	}
	if !errors.Is(gotRotationErr, rotationErr) {
		t.Fatalf("expected rotation error %v, got %v", rotationErr, gotRotationErr)
	}
	if sessionErr != nil {
		t.Fatalf("expected session verification to succeed, got %v", sessionErr)
	}
	if !healthy {
		t.Fatal("expected provider to remain healthy when Gemini still accepts the session")
	}
}

func TestMergeCookieHeadersPreservesFullSessionAndOverridesAuth(t *testing.T) {
	got := mergeCookieHeaders(
		"NID=rollout; __Secure-1PSID=old; SIDCC=account",
		"__Secure-1PSID=new; __Secure-1PSIDTS=fresh",
	)
	for _, want := range []string{"NID=rollout", "SIDCC=account", "__Secure-1PSID=new", "__Secure-1PSIDTS=fresh"} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged cookies %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "__Secure-1PSID=old") {
		t.Fatalf("old auth cookie survived override: %q", got)
	}
}

func TestRefreshSessionHealthMarksUnhealthyWhenBothChecksFail(t *testing.T) {
	_, _, healthy := refreshSessionHealth(
		func() error { return errors.New("rotation failed") },
		func() error { return errors.New("session verification failed") },
	)

	if healthy {
		t.Fatal("expected provider to be unhealthy when both refresh checks fail")
	}
}

func TestParseResponseExtractsGeneratedImages(t *testing.T) {
	imageURL := "https://lh3.googleusercontent.com/generated-image=w1024-h1024"
	iconURL := "https://fonts.gstatic.com/s/i/short-term/release/googlesymbols/expand/default/24px.svg"
	payload := []interface{}{
		nil,
		"conversation-id",
		nil,
		nil,
		[]interface{}{
			[]interface{}{
				"response-id",
				[]interface{}{"done", []interface{}{iconURL, imageURL}},
			},
		},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	root := []interface{}{
		[]interface{}{nil, nil, string(payloadJSON)},
	}
	rootJSON, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}

	client := &Client{log: zap.NewNop()}
	resp, err := client.parseResponse(string(rootJSON))
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}

	if resp.Text != "done" {
		t.Fatalf("expected text %q, got %q", "done", resp.Text)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(resp.Images))
	}
	if resp.Images[0].URL != imageURL {
		t.Fatalf("expected image URL %q, got %q", imageURL, resp.Images[0].URL)
	}
}

func TestNormalizeImageURLHandlesGoogleusercontentReferences(t *testing.T) {
	tests := map[string]string{
		"http://googleusercontent.com/image_generation_content/211": "",
		"googleusercontent.com/image_generation_content/211":        "",
		"//lh3.googleusercontent.com/generated-image=w1024-h1024":   "https://lh3.googleusercontent.com/generated-image=w1024-h1024",
	}

	for input, want := range tests {
		if got := normalizeImageURL(input); got != want {
			t.Fatalf("normalizeImageURL(%q): expected %q, got %q", input, want, got)
		}
	}
}

func TestParseResponseHandlesBardErrorInfo(t *testing.T) {
	errPayload := `)]}'

	[["wrb.fr",null,null,null,null,[13,null,[["type.googleapis.com/assistant.boq.bard.application.BardErrorInfo",[1152]]]]],["di",2461]]`

	client := &Client{log: zap.NewNop()}
	_, err := client.parseResponse(errPayload)
	if err == nil {
		t.Fatal("Expected parseResponse to fail with error")
	}

	expectedSubstr := "BardErrorInfo code [13 1152]"
	if !strings.Contains(err.Error(), expectedSubstr) {
		t.Fatalf("Expected error to contain %q, got: %v", expectedSubstr, err)
	}
}

func buildWireLine(t *testing.T, payload []interface{}) string {
	t.Helper()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	root := []interface{}{
		[]interface{}{nil, nil, string(payloadJSON)},
	}
	rootJSON, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return string(rootJSON)
}

func TestParseResponseExtractsUsageMetadata(t *testing.T) {
	payload := []interface{}{
		nil,
		"conversation-id",
		nil,
		nil,
		[]interface{}{
			[]interface{}{"response-id", []interface{}{"done"}},
		},
		map[string]interface{}{
			"usageMetadata": map[string]interface{}{
				"promptTokenCount":     11.0,
				"candidatesTokenCount": 7.0,
				"totalTokenCount":      18.0,
			},
		},
	}

	client := &Client{log: zap.NewNop()}
	resp, err := client.parseResponse(buildWireLine(t, payload))
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected usage metadata, got nil")
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v, want 11/7/18", resp.Usage)
	}
}

func TestParseResponseWithoutUsageMetadataLeavesItNil(t *testing.T) {
	payload := []interface{}{
		nil,
		"conversation-id",
		nil,
		nil,
		[]interface{}{
			[]interface{}{"response-id", []interface{}{"done"}},
		},
	}

	client := &Client{log: zap.NewNop()}
	resp, err := client.parseResponse(buildWireLine(t, payload))
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}
	if resp.Usage != nil {
		t.Fatalf("usage = %+v, want nil when the wire format carries no counts", resp.Usage)
	}
}

// TestParseResponseStripsCitationArtifacts proves the sanitize step is wired into
// the provider's shared text path, so every endpoint served from Response.Text
// gets clean output.
func TestParseResponseStripsCitationArtifacts(t *testing.T) {
	const raw = "Here is the total [cite: 3, 7]:\n\n" +
		"```python?code_reference&code_event_index=1\nprint(2 + 2)\n\n```\n\n" +
		"```text?code_stdout&code_event_index=1\n4\n```\n\n" +
		"That is all [cite_start]done."
	const want = "Here is the total:\n\n" +
		"```python\nprint(2 + 2)\n\n```\n\n" +
		"```text\n4\n```\n\n" +
		"That is all done."

	payload := []interface{}{
		nil,
		"conversation-id",
		nil,
		nil,
		[]interface{}{
			[]interface{}{"response-id", []interface{}{raw}},
		},
	}

	client := &Client{log: zap.NewNop()}
	resp, err := client.parseResponse(buildWireLine(t, payload))
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}
	if resp.Text != want {
		t.Fatalf("text = %q, want %q", resp.Text, want)
	}
}

// TestParseResponseStripsCitationsFromCanvasDocuments keeps a canvas the same
// inline and as a file: documents lose their markers, code stays verbatim.
func TestParseResponseStripsCitationsFromCanvasDocuments(t *testing.T) {
	const code = `const note = "Hello [cite: 1]";`
	doc := canvasTestEntry(false)
	doc[4] = "Coffee [cite_start]**really** is great [cite: 1]."
	codeEntry := canvasTestEntry(true)
	codeEntry[4] = code
	text := "Here [cite: 2]:\n\nhttp://googleusercontent.com/immersive_entry_chip/0\n\nhttp://googleusercontent.com/immersive_entry_chip/1"
	payload := []interface{}{nil, "conversation-id", nil, nil, []interface{}{canvasTestCandidate(text, doc, codeEntry)}}

	resp, err := (&Client{log: zap.NewNop()}).parseResponse(buildWireLine(t, payload))
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}
	if len(resp.Canvases) != 2 {
		t.Fatalf("canvases = %#v", resp.Canvases)
	}
	if got := resp.Canvases[0].Content; got != "Coffee **really** is great." {
		t.Fatalf("document canvas = %q", got)
	}
	if got := resp.Canvases[1].Content; got != code {
		t.Fatalf("code canvas = %q, want it verbatim", got)
	}
	want := "Here:\n\nCoffee **really** is great.\n\n```html\n" + code + "\n```"
	if resp.Text != want {
		t.Fatalf("text = %q, want %q", resp.Text, want)
	}
}

const testPSID = "test-psid"

func writeCookieCache(t *testing.T, psid, ts string) string {
	t.Helper()
	if err := os.MkdirAll(".cookies", 0o755); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(psid))
	name := filepath.Join(".cookies", hex.EncodeToString(hash[:])+".txt")
	if err := os.WriteFile(name, []byte(ts), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func readCookieCache(t *testing.T, psid string) string {
	t.Helper()
	hash := sha256.Sum256([]byte(psid))
	data, err := os.ReadFile(filepath.Join(".cookies", hex.EncodeToString(hash[:])+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func newInitTestClient(configTS string, refreshFn func(currentTS string) error) *Client {
	c := &Client{
		httpClient: req.NewClient(),
		cookies: &CookieStore{
			Secure1PSID:   testPSID,
			Secure1PSIDTS: configTS,
		},
		log: zap.NewNop(),
	}
	c.refreshSessionTokenFn = func(ctx context.Context) error {
		return refreshFn(c.cookies.Secure1PSIDTS)
	}
	return c
}

func TestInitValidConfigOverwritesCache(t *testing.T) {
	t.Chdir(t.TempDir())
	writeCookieCache(t, testPSID, "cache-ts")

	c := newInitTestClient("config-ts", func(string) error { return nil })
	if err := c.Init(context.Background()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if got := readCookieCache(t, testPSID); got != "config-ts" {
		t.Fatalf("cache = %q, want config TS %q", got, "config-ts")
	}
}

func TestInitFallsBackToCacheWhenConfigRejected(t *testing.T) {
	t.Chdir(t.TempDir())
	writeCookieCache(t, testPSID, "cache-ts")

	var attempts []string
	c := newInitTestClient("config-ts", func(currentTS string) error {
		attempts = append(attempts, currentTS)
		if currentTS == "config-ts" {
			return errors.New("config rejected")
		}
		return nil
	})
	c.rotateFn = func() error { return errors.New("rotation must not run when cache succeeds") }

	if err := c.Init(context.Background()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if len(attempts) != 2 || attempts[0] != "config-ts" || attempts[1] != "cache-ts" {
		t.Fatalf("attempts = %v, want [config-ts cache-ts]", attempts)
	}
	if got := readCookieCache(t, testPSID); got != "cache-ts" {
		t.Fatalf("cache = %q, want cached TS preserved %q", got, "cache-ts")
	}
}

func TestInitFailsWhenConfigAndCacheStaleAndPreservesCache(t *testing.T) {
	t.Chdir(t.TempDir())
	cacheFile := writeCookieCache(t, testPSID, "cache-ts")

	c := newInitTestClient("config-ts", func(string) error { return errors.New("config rejected") })
	c.rotateFn = func() error { return errors.New("rotation failed") }

	err := c.Init(context.Background())
	if err == nil {
		t.Fatal("Init succeeded, want error")
	}
	if err.Error() != "config rejected" {
		t.Fatalf("Init error = %v, want the config attempt error", err)
	}
	if got := readCookieCache(t, testPSID); got != "cache-ts" {
		t.Fatalf("cache = %q after failed init, want untouched %q", got, "cache-ts")
	}
	if info, statErr := os.Stat(cacheFile); statErr != nil || info.Size() == 0 {
		t.Fatalf("cache file missing or empty after failed init: %v", statErr)
	}
}
