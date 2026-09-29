package providers

import (
	"encoding/json"
	"errors"
	"net/http"
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

func TestUpdateCookiesSynchronizesHeadersAndStore(t *testing.T) {
	client := &Client{
		cookies: &CookieStore{
			Secure1PSID:   "test_psid",
			Secure1PSIDTS: "old_ts",
		},
		cookieHeader:           "__Secure-1PSID=test_psid; __Secure-1PSIDTS=old_ts",
		configuredCookieHeader: "__Secure-1PSID=test_psid; __Secure-1PSIDTS=old_ts",
		httpClient:             req.NewClient(),
		log:                    zap.NewNop(),
	}

	client.updateCookies([]*http.Cookie{
		{Name: "__Secure-1PSIDTS", Value: "new_ts"},
		{Name: "__Secure-3PSIDTS", Value: "new_3ts"},
		{Name: "SIDCC", Value: "new_sidcc"},
	})

	if client.cookies.Secure1PSIDTS != "new_ts" {
		t.Fatalf("expected Secure1PSIDTS to be 'new_ts', got %q", client.cookies.Secure1PSIDTS)
	}
	if !strings.Contains(client.cookieHeader, "__Secure-1PSIDTS=new_ts") {
		t.Fatalf("cookieHeader missing updated 1PSIDTS: %q", client.cookieHeader)
	}
	if !strings.Contains(client.cookieHeader, "__Secure-3PSIDTS=new_3ts") {
		t.Fatalf("cookieHeader missing updated 3PSIDTS: %q", client.cookieHeader)
	}
	if !strings.Contains(client.cookieHeader, "SIDCC=new_sidcc") {
		t.Fatalf("cookieHeader missing updated SIDCC: %q", client.cookieHeader)
	}
	_ = client.ClearCookieCache()
}

