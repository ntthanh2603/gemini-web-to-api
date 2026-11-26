package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestExtractGeneratedImagesFromCandidateMediaSlot(t *testing.T) {
	meta := []interface{}{nil, float64(1), "generated.png", "https://lh3.googleusercontent.com/generated-image", nil, "image/png", []interface{}{float64(1024), float64(768), float64(12345)}}
	imageEntry := []interface{}{[]interface{}{nil, nil, nil, meta}}
	slot := make([]interface{}, 8)
	slot[7] = []interface{}{[]interface{}{imageEntry}}
	candidate := make([]interface{}, 13)
	candidate[12] = slot

	got := extractGeneratedImages(candidate)
	if len(got) != 1 {
		t.Fatalf("expected one generated image, got %d", len(got))
	}
	if got[0].URL != "https://lh3.googleusercontent.com/generated-image" {
		t.Fatalf("unexpected URL: %q", got[0].URL)
	}
	if !got[0].Generated {
		t.Fatal("structured media image must be marked generated")
	}
	if got[0].MimeType != "image/png" || got[0].Width != 1024 || got[0].Height != 768 {
		t.Fatalf("unexpected metadata: %+v", got[0])
	}
}

func TestParseResponseUsesGeneratedImageMediaSlot(t *testing.T) {
	meta := []interface{}{nil, float64(1), "generated.png", "https://lh3.googleusercontent.com/generated-image", nil, "image/png", []interface{}{float64(1024), float64(768), float64(12345)}}
	imageEntry := []interface{}{[]interface{}{nil, nil, nil, meta}}
	slot := make([]interface{}, 8)
	slot[7] = []interface{}{[]interface{}{imageEntry}}
	candidate := make([]interface{}, 13)
	candidate[0] = "response-id"
	candidate[1] = []interface{}{"", nil}
	candidate[12] = slot
	payload := make([]interface{}, 5)
	payload[1] = "conversation-id"
	payload[4] = []interface{}{candidate}
	payloadJSON, _ := json.Marshal(payload)
	rootJSON, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", "rpc", string(payloadJSON)}})

	client := &Client{}
	resp, err := client.parseResponse(string(rootJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("expected one generated image, got %d", len(resp.Images))
	}
}

func TestAppendGoogleImageSizePreservesURLParts(t *testing.T) {
	if got := appendGoogleImageSize("https://lh3.googleusercontent.com/foo?download=1#fragment", 2048); got != "https://lh3.googleusercontent.com/foo=s2048?download=1#fragment" {
		t.Fatalf("unexpected URL: %q", got)
	}
	if got := appendGoogleImageSize("https://lh3.googleusercontent.com/foo=w1024-h1024?download=1", 2048); got != "https://lh3.googleusercontent.com/foo=w1024-h1024?download=1" {
		t.Fatalf("existing transform must be preserved: %q", got)
	}
}

func TestIsTrustedGoogleMediaHost(t *testing.T) {
	trusted := []string{
		"lh3.googleusercontent.com",
		"work.fife.usercontent.google.com",
		"gemini.google.com",
	}
	for _, host := range trusted {
		if !isTrustedGoogleMediaHost(host) {
			t.Fatalf("expected trusted host: %s", host)
		}
	}

	untrusted := []string{
		"googleusercontent.com.evil.example",
		"google.com.evil.example",
		"evil.example",
		"",
	}
	for _, host := range untrusted {
		if isTrustedGoogleMediaHost(host) {
			t.Fatalf("expected untrusted host: %s", host)
		}
	}
}

func TestDownloadGeneratedImageRejectsUntrustedHostBeforeRequest(t *testing.T) {
	client := &Client{}
	_, err := client.downloadGeneratedImage(context.Background(), "https://evil.example/image.png", "secret-cookie")
	if err == nil || !strings.Contains(err.Error(), "untrusted host") {
		t.Fatalf("expected untrusted host rejection, got %v", err)
	}
}

func TestGeneratedImageRedirectPolicyRejectsUntrustedHost(t *testing.T) {
	client := generatedImageHTTPClient("secret-cookie")
	request, err := http.NewRequest(http.MethodGet, "https://evil.example/image.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	via, _ := http.NewRequest(http.MethodGet, "https://lh3.googleusercontent.com/image", nil)
	if err := client.CheckRedirect(request, []*http.Request{via}); err == nil {
		t.Fatal("expected untrusted redirect rejection")
	}
}

func TestGeneratedImageRedirectPolicyAddsCookieOnlyToTrustedHost(t *testing.T) {
	client := generatedImageHTTPClient("secret-cookie")
	request, err := http.NewRequest(http.MethodGet, "https://work.fife.usercontent.google.com/image", nil)
	if err != nil {
		t.Fatal(err)
	}
	via, _ := http.NewRequest(http.MethodGet, "https://lh3.googleusercontent.com/image", nil)
	if err := client.CheckRedirect(request, []*http.Request{via}); err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("Cookie"); got != "secret-cookie" {
		t.Fatalf("expected cookie on trusted redirect, got %q", got)
	}
}

func TestReadGeneratedImageBodyRejectsOversize(t *testing.T) {
	reader := io.LimitReader(strings.NewReader(strings.Repeat("x", maxGeneratedImageBytes+1)), maxGeneratedImageBytes+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= maxGeneratedImageBytes {
		t.Fatal("test fixture was not oversized")
	}
}

func TestExtractGeneratedImagesReadsImageIDAndToleratesMissingSlot(t *testing.T) {
	// Real Gemini Web responses carry the image_generation_content id at
	// imageNode[1][0]; it is what the full-size download RPC needs.
	meta := []interface{}{nil, float64(1), "generated.png", "https://lh3.googleusercontent.com/generated-image", nil, "image/png"}
	otherMeta := []interface{}{nil, float64(1), "second.png", "https://lh3.googleusercontent.com/second-image", nil, "image/png"}
	withID := []interface{}{[]interface{}{nil, nil, nil, meta}, []interface{}{"0_1050", 0}}
	withoutID := []interface{}{[]interface{}{nil, nil, nil, otherMeta}}
	slot := make([]interface{}, 8)
	slot[7] = []interface{}{[]interface{}{withID, withoutID}}
	candidate := make([]interface{}, 13)
	candidate[12] = slot

	got := extractGeneratedImages(candidate)
	if len(got) != 2 {
		t.Fatalf("expected two generated images, got %d", len(got))
	}
	if got[0].ImageID != "0_1050" {
		t.Fatalf("image id not read: %q", got[0].ImageID)
	}
	if got[1].ImageID != "" {
		t.Fatalf("missing id slot must not panic or invent an id: %q", got[1].ImageID)
	}
}

func TestScopedCookieForHostFollowsBrowserDomainRules(t *testing.T) {
	main := "SID=x; __Secure-1PSID=y"
	osid := "OSID=a; __Secure-OSID=b"
	cases := map[string]string{
		"lh3.googleusercontent.com":           osid,
		"work.fife.usercontent.google.com":    main,
		"lh3.google.com":                      main,
		"contribution.usercontent.google.com": main,
	}
	for host, want := range cases {
		if got := scopedCookieForHost(host, main, osid); got != want {
			t.Errorf("scopedCookieForHost(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestSplitScopedCookiesSeparatesOSIDPair(t *testing.T) {
	header := "SID=x; OSID=a; __Secure-1PSID=y; __Secure-OSID=b; NID=c"
	main, osid := splitScopedCookies(header)
	if osid != "OSID=a; __Secure-OSID=b" {
		t.Fatalf("osid set = %q", osid)
	}
	for _, want := range []string{"SID=x", "__Secure-1PSID=y", "NID=c"} {
		if !strings.Contains(main, want) {
			t.Fatalf("main set %q missing %q", main, want)
		}
	}
	if strings.Contains(main, "OSID") {
		t.Fatalf("main set must not contain OSID cookies: %q", main)
	}
}

func TestBuildDownloadEntryURLAddsWebUIDownloadMarkers(t *testing.T) {
	got, err := buildDownloadEntryURL("https://lh3.googleusercontent.com/gg-dl/abc", "6")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://lh3.googleusercontent.com/gg-dl/abc=d-I?") {
		t.Fatalf("entry url missing =d-I marker: %q", got)
	}
	if !strings.Contains(got, "alr=yes") || !strings.Contains(got, "authuser=6") {
		t.Fatalf("entry url missing alr/authuser: %q", got)
	}
	if got, _ = buildDownloadEntryURL("https://lh3.googleusercontent.com/gg-dl/abc", ""); strings.Contains(got, "authuser") {
		t.Fatalf("no account slot must not add authuser: %q", got)
	}
}
