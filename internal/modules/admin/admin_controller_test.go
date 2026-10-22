package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gemini-web-to-api/internal/commons/configs"
	"gemini-web-to-api/internal/modules/providers"
	"gemini-web-to-api/internal/server"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

const (
	testAPIKey  = "test-admin-key"
	testPSID    = "current-psid-value"
	testPSIDTS  = "current-ts-value"
	adminPath   = "/admin/cookies"
	jsonHeaders = "application/json"
)

type testHarness struct {
	app       *fiber.App
	client    *providers.Client
	apiKey    string
	checkHits int
}

// errRejected mimics the error Init returns when Google refuses the session.
var errRejected = errors.New("authentication failed: SNlM0e not found")

// newHarness builds the app with the real controller and a provider client
// whose Google session check is faked, so tests never touch the network.
func newHarness(t *testing.T, apiKey string, check func(ctx context.Context, header string) error) *testHarness {
	t.Helper()
	t.Chdir(t.TempDir())

	h := &testHarness{apiKey: apiKey}

	cfg := &configs.Config{Gemini: configs.GeminiConfig{
		Cookies:         "NID=extra; __Secure-1PSID=" + testPSID + "; __Secure-1PSIDTS=" + testPSIDTS,
		Secure1PSID:     testPSID,
		Secure1PSIDTS:   testPSIDTS,
		CookieAPIKey:    apiKey,
		RefreshInterval: 60,
		MaxRetries:      1,
	}}

	h.client = providers.NewClientWithSessionValidator(cfg, zap.NewNop(), func(ctx context.Context, header string) error {
		h.checkHits++
		if check != nil {
			return check(ctx, header)
		}
		return nil
	})

	app := fiber.New()
	NewController(h.client, cfg, zap.NewNop()).Register(app)
	server.Register404Handler(app)
	h.app = app
	return h
}

func (h *testHarness) do(t *testing.T, method, path, body string, auth bool) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", jsonHeaders)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
	}

	resp, err := h.app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, payload
}

func updateBody(header string) string {
	raw, err := json.Marshal(map[string]string{"cookies": header})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestAdminRoutesAbsentWithoutAPIKey(t *testing.T) {
	h := newHarness(t, "", nil)

	for _, method := range []string{http.MethodPost, http.MethodGet} {
		status, _ := h.do(t, method, adminPath, updateBody("__Secure-1PSID=x; __Secure-1PSIDTS=y"), true)
		if status != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404 when GEMINI_COOKIE_API_KEY is unset", method, adminPath, status)
		}
	}
	if h.checkHits != 0 {
		t.Fatalf("session checks = %d, want 0", h.checkHits)
	}
}

func TestAdminRoutesRejectBadToken(t *testing.T) {
	h := newHarness(t, testAPIKey, nil)

	cases := []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "wrong token", header: "Bearer wrong-key"},
		{name: "wrong scheme", header: "Basic " + testAPIKey},
		{name: "empty token", header: "Bearer "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, adminPath, strings.NewReader(updateBody("__Secure-1PSID=x; __Secure-1PSIDTS=y")))
			req.Header.Set("Content-Type", jsonHeaders)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := h.app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}

			status, _ := h.do(t, http.MethodGet, adminPath, "", false)
			if status != http.StatusUnauthorized {
				t.Fatalf("GET without token = %d, want 401", status)
			}
		})
	}

	if h.checkHits != 0 {
		t.Fatalf("session checks = %d, want 0", h.checkHits)
	}
}

func TestAdminRejectsMalformedBody(t *testing.T) {
	h := newHarness(t, testAPIKey, nil)

	status, body := h.do(t, http.MethodPost, adminPath, `{"cookies":`, true)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, body)
	}
	if !strings.Contains(string(body), "error") {
		t.Fatalf("body = %s, want an error field", body)
	}
	if h.checkHits != 0 {
		t.Fatalf("session checks = %d, want 0", h.checkHits)
	}
}

func TestAdminRejectsMissingRequiredCookiesAndKeepsSession(t *testing.T) {
	h := newHarness(t, testAPIKey, nil)

	_, before := h.do(t, http.MethodGet, adminPath, "", true)

	status, body := h.do(t, http.MethodPost, adminPath, updateBody("NID=extra; __Secure-1PSIDTS=fresh-ts"), true)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, body)
	}
	if !strings.Contains(string(body), "__Secure-1PSID") {
		t.Fatalf("body = %s, want the missing cookie named", body)
	}

	status, body = h.do(t, http.MethodPost, adminPath, updateBody("NID=extra; __Secure-1PSID=fresh-psid"), true)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, body)
	}
	if !strings.Contains(string(body), "__Secure-1PSIDTS") {
		t.Fatalf("body = %s, want the missing cookie named", body)
	}

	if h.client.GetCookies().Secure1PSID != testPSID || h.client.GetCookies().Secure1PSIDTS != testPSIDTS {
		t.Fatalf("session = %+v, want the original pair untouched", h.client.GetCookies())
	}
	if h.checkHits != 0 {
		t.Fatalf("session checks = %d, want 0", h.checkHits)
	}

	_, after := h.do(t, http.MethodGet, adminPath, "", true)
	if fingerprintOf(before) != fingerprintOf(after) {
		t.Fatalf("fingerprint changed: %s -> %s", fingerprintOf(before), fingerprintOf(after))
	}
}

func TestAdminSamePSIDTSIsNoopWithoutGoogleCall(t *testing.T) {
	h := newHarness(t, testAPIKey, nil)

	status, body := h.do(t, http.MethodPost, adminPath,
		updateBody("NID=rotated; __Secure-1PSID="+testPSID+"; __Secure-1PSIDTS="+testPSIDTS), true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, body)
	}
	if got := decodeMap(t, body)["status"]; got != string(providers.CookieUpdateNoop) {
		t.Fatalf("status field = %v, want %q", got, providers.CookieUpdateNoop)
	}
	if h.checkHits != 0 {
		t.Fatalf("session checks = %d, want 0 for a same-PSIDTS push", h.checkHits)
	}
	if cookies := h.client.GetCookies(); cookies.Secure1PSIDTS != testPSIDTS {
		t.Fatalf("psidts = %q, want unchanged", cookies.Secure1PSIDTS)
	}
}

func TestAdminHappyPathSwapsAndPersistsSession(t *testing.T) {
	var checked string
	h := newHarness(t, testAPIKey, func(_ context.Context, header string) error {
		checked = header
		return nil
	})

	const newPSID = "fresh-psid-value"
	const newPSIDTS = "fresh-ts-value"
	header := "NID=extra; __Secure-1PSID=" + newPSID + "; __Secure-1PSIDTS=" + newPSIDTS

	status, body := h.do(t, http.MethodPost, adminPath, updateBody(header), true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, body)
	}
	payload := decodeMap(t, body)
	if payload["status"] != string(providers.CookieUpdateApplied) {
		t.Fatalf("status field = %v, want %q", payload["status"], providers.CookieUpdateApplied)
	}
	if payload["source"] != providers.CookieSourceAdminAPI {
		t.Fatalf("source field = %v, want %q", payload["source"], providers.CookieSourceAdminAPI)
	}
	if checked != header {
		t.Fatalf("session check header = %q, want the pushed header", checked)
	}
	if h.checkHits != 1 {
		t.Fatalf("session checks = %d, want 1", h.checkHits)
	}

	cookies := h.client.GetCookies()
	if cookies.Secure1PSID != newPSID || cookies.Secure1PSIDTS != newPSIDTS {
		t.Fatalf("session = %+v, want the pushed pair", cookies)
	}

	hash := sha256.Sum256([]byte(newPSID))
	cachePath := filepath.Join(".cookies", hex.EncodeToString(hash[:])+".txt")
	persisted, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("reading cookie cache: %v", err)
	}
	if string(persisted) != newPSIDTS {
		t.Fatalf("cache = %q, want %q", string(persisted), newPSIDTS)
	}

	status, body = h.do(t, http.MethodGet, adminPath, "", true)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want 200 (body %s)", status, body)
	}
	if strings.Contains(string(body), newPSID) || strings.Contains(string(body), newPSIDTS) {
		t.Fatalf("GET body leaks cookie material: %s", body)
	}
}

func TestAdminRejectedCookiesLeaveSessionUntouched(t *testing.T) {
	h := newHarness(t, testAPIKey, func(context.Context, string) error {
		return errRejected
	})

	_, before := h.do(t, http.MethodGet, adminPath, "", true)

	status, body := h.do(t, http.MethodPost, adminPath,
		updateBody("__Secure-1PSID=bad-psid; __Secure-1PSIDTS=bad-ts"), true)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, body)
	}
	if strings.Contains(string(body), "bad-psid") || strings.Contains(string(body), "bad-ts") {
		t.Fatalf("error body leaks cookie material: %s", body)
	}

	_, after := h.do(t, http.MethodGet, adminPath, "", true)
	if fingerprintOf(before) != fingerprintOf(after) {
		t.Fatalf("fingerprint changed after a rejected update")
	}
	if cookies := h.client.GetCookies(); cookies.Secure1PSIDTS != testPSIDTS {
		t.Fatalf("psidts = %q, want untouched", cookies.Secure1PSIDTS)
	}
}

func TestAdminStatusReturnsRedactedFingerprint(t *testing.T) {
	h := newHarness(t, testAPIKey, nil)

	status, body := h.do(t, http.MethodGet, adminPath, "", true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, body)
	}

	payload := decodeMap(t, body)
	fingerprint, _ := payload["psid_fingerprint"].(string)
	sum := sha256.Sum256([]byte(testPSID))
	want := hex.EncodeToString(sum[:])[:12]
	if fingerprint != want {
		t.Fatalf("psid_fingerprint = %q, want %q", fingerprint, want)
	}

	age, ok := payload["cache_age_seconds"].(float64)
	if !ok || age < 0 {
		t.Fatalf("cache_age_seconds = %v, want a non-negative number", payload["cache_age_seconds"])
	}
	updatedAt, _ := payload["updated_at"].(string)
	if _, err := time.Parse(time.RFC3339, updatedAt); err != nil {
		t.Fatalf("updated_at = %q, want RFC3339: %v", updatedAt, err)
	}
	if payload["source"] != providers.CookieSourceEnv {
		t.Fatalf("source = %v, want %q", payload["source"], providers.CookieSourceEnv)
	}
	if strings.Contains(string(body), testPSID) || strings.Contains(string(body), testPSIDTS) {
		t.Fatalf("status body leaks cookie material: %s", body)
	}
}

func decodeMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(body), &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return out
}

func fingerprintOf(body []byte) string {
	var out struct {
		Fingerprint string `json:"psid_fingerprint"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Fingerprint
}
