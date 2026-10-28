package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gemini-web-to-api/internal/commons/configs"

	"go.uber.org/zap"
)

const (
	testCookiePSID = "old-psid-value"
	testCookieTS   = "old-ts-value"
)

func newCookieTestClient(t *testing.T, validate func(ctx context.Context, cookieHeader string) error) *Client {
	t.Helper()
	t.Chdir(t.TempDir())

	cfg := &configs.Config{Gemini: configs.GeminiConfig{
		Cookies:         "NID=extra; __Secure-1PSID=" + testCookiePSID + "; __Secure-1PSIDTS=" + testCookieTS,
		Secure1PSID:     testCookiePSID,
		Secure1PSIDTS:   testCookieTS,
		RefreshInterval: 60,
		MaxRetries:      1,
	}}
	if validate == nil {
		validate = func(context.Context, string) error {
			t.Error("session check must not run in this test")
			return errors.New("unexpected session check")
		}
	}
	return NewClientWithSessionValidator(cfg, zap.NewNop(), validate)
}

func cookieCacheFile(psid string) string {
	hash := sha256.Sum256([]byte(psid))
	return filepath.Join(".cookies", hex.EncodeToString(hash[:])+".txt")
}

// markHealthy marks c's session live, as a successful Init would.
func markHealthy(c *Client) {
	c.mu.Lock()
	c.healthy = true
	c.mu.Unlock()
}

func TestUpdateCookiesReplacesConfiguredHeaderForLaterRefreshes(t *testing.T) {
	c := newCookieTestClient(t, func(context.Context, string) error { return nil })
	c.mu.Lock()
	c.configuredCookieHeader = "SID=old-sid; SAPISID=old-sapisid; __Secure-1PSID=" + testCookiePSID + "; __Secure-1PSIDTS=" + testCookieTS
	c.mu.Unlock()

	pushed := "SID=new-sid; SAPISID=new-sapisid; __Secure-1PSID=new-psid; __Secure-1PSIDTS=new-ts"
	if _, err := c.UpdateCookies(context.Background(), pushed); err != nil {
		t.Fatal(err)
	}

	// refreshSessionToken rebuilds the session from configuredCookieHeader.
	c.mu.RLock()
	configured := c.configuredCookieHeader
	c.mu.RUnlock()
	next := mergeCookieHeaders(configured, "__Secure-1PSID=new-psid; __Secure-1PSIDTS=new-ts")
	if strings.Contains(next, "old-sid") || strings.Contains(next, "old-sapisid") {
		t.Fatalf("next refresh would send %q, want no cookies from GEMINI_COOKIES", next)
	}
}

func TestUpdateCookiesRechecksSamePSIDTSWhenUnhealthy(t *testing.T) {
	checks := 0
	c := newCookieTestClient(t, func(context.Context, string) error {
		checks++
		return nil
	}) // not initialized, so not healthy

	result, err := c.UpdateCookies(context.Background(),
		"SID=fresh; __Secure-1PSID="+testCookiePSID+"; __Secure-1PSIDTS="+testCookieTS)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CookieUpdateApplied || checks != 1 {
		t.Fatalf("status = %q after %d checks, want %q after 1", result.Status, checks, CookieUpdateApplied)
	}
	if !c.IsHealthy() {
		t.Fatal("client still unhealthy after a validated push")
	}
}

func TestUpdateCookiesWaitsForOtherSessionChanges(t *testing.T) {
	c := newCookieTestClient(t, func(context.Context, string) error { return nil })

	c.sessionMu.Lock() // the refresh loop or another push is mid-change
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.UpdateCookies(context.Background(), "__Secure-1PSID=new-psid; __Secure-1PSIDTS=new-ts")
	}()

	select {
	case <-done:
		t.Fatal("UpdateCookies ran while another session change held sessionMu")
	case <-time.After(50 * time.Millisecond):
	}
	if got := c.GetCookies().Secure1PSIDTS; got != testCookieTS {
		t.Fatalf("psidts = %q while blocked, want %q", got, testCookieTS)
	}

	c.sessionMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("UpdateCookies did not finish after sessionMu was released")
	}
	if got := c.GetCookies().Secure1PSIDTS; got != "new-ts" {
		t.Fatalf("psidts = %q, want the pushed value", got)
	}
}

// restartedClient builds a fresh client in the current directory, as a
// restart with GEMINI_COOKIES=envCookies would, with Init's Google calls
// faked to succeed.
func restartedClient(envCookies string) *Client {
	cfg := &configs.Config{Gemini: configs.GeminiConfig{
		Cookies:         envCookies,
		Secure1PSID:     cookieHeaderValue(envCookies, "__Secure-1PSID"),
		Secure1PSIDTS:   cookieHeaderValue(envCookies, "__Secure-1PSIDTS"),
		RefreshInterval: 60,
		MaxRetries:      1,
	}}
	c := NewClientWithSessionValidator(cfg, zap.NewNop(), nil)
	c.autoRefresh = false
	c.refreshSessionTokenFn = func(context.Context) error { return nil }
	return c
}

func TestPushedSessionSurvivesRestart(t *testing.T) {
	c := newCookieTestClient(t, func(context.Context, string) error { return nil })
	pushed := "SID=new-sid; __Secure-1PSID=new-psid; __Secure-1PSIDTS=new-ts"
	if _, err := c.UpdateCookies(context.Background(), pushed); err != nil {
		t.Fatal(err)
	}

	restarted := restartedClient("NID=extra; __Secure-1PSID=" + testCookiePSID + "; __Secure-1PSIDTS=" + testCookieTS)
	if err := restarted.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	cookies := restarted.GetCookies()
	if cookies.Secure1PSID != "new-psid" || cookies.Secure1PSIDTS != "new-ts" || cookies.Source != CookieSourceAdminAPI {
		t.Fatalf("cookies after restart = %+v, want the pushed session", cookies)
	}
	restarted.mu.RLock()
	configured := restarted.configuredCookieHeader
	restarted.mu.RUnlock()
	if configured != pushed {
		t.Fatalf("configured header after restart = %q, want the pushed header", configured)
	}
}

func TestPushedSessionIgnoredAfterGeminiCookiesChange(t *testing.T) {
	c := newCookieTestClient(t, func(context.Context, string) error { return nil })
	if _, err := c.UpdateCookies(context.Background(), "__Secure-1PSID=new-psid; __Secure-1PSIDTS=new-ts"); err != nil {
		t.Fatal(err)
	}

	edited := "__Secure-1PSID=edited-psid; __Secure-1PSIDTS=edited-ts"
	restarted := restartedClient(edited)
	if err := restarted.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cookies := restarted.GetCookies(); cookies.Secure1PSID != "edited-psid" || cookies.Source != CookieSourceEnv {
		t.Fatalf("cookies after restart = %+v, want the edited GEMINI_COOKIES", cookies)
	}
}

func TestUpdateCookiesRejectsMissingRequiredCookies(t *testing.T) {
	c := newCookieTestClient(t, nil)

	cases := map[string]struct {
		header string
		want   string
	}{
		"missing psid":   {header: "NID=extra; __Secure-1PSIDTS=new-ts", want: "__Secure-1PSID"},
		"missing psidts": {header: "NID=extra; __Secure-1PSID=new-psid", want: "__Secure-1PSIDTS"},
		"empty header":   {header: "   ", want: "__Secure-1PSID"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := c.UpdateCookies(context.Background(), tc.header)
			var invalid *CookieValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want *CookieValidationError", err)
			}
			if !strings.Contains(invalid.Reason, tc.want) {
				t.Fatalf("reason = %q, want it to mention %q", invalid.Reason, tc.want)
			}
		})
	}

	cookies := c.GetCookies()
	if cookies.Secure1PSID != testCookiePSID || cookies.Secure1PSIDTS != testCookieTS {
		t.Fatalf("cookies = %+v, want the original pair untouched", cookies)
	}
	if _, err := os.Stat(cookieCacheFile(testCookiePSID)); !os.IsNotExist(err) {
		t.Fatalf("cache file unexpectedly written: %v", err)
	}
}

func TestUpdateCookiesNoopWhenPSIDTSUnchanged(t *testing.T) {
	c := newCookieTestClient(t, nil) // any session check fails the test
	markHealthy(c)

	result, err := c.UpdateCookies(context.Background(),
		"__Secure-1PSID="+testCookiePSID+"; __Secure-1PSIDTS="+testCookieTS+"; NID=rotated")
	if err != nil {
		t.Fatalf("UpdateCookies returned error: %v", err)
	}
	if result.Status != CookieUpdateNoop {
		t.Fatalf("status = %q, want %q", result.Status, CookieUpdateNoop)
	}
	if result.Source != CookieSourceEnv {
		t.Fatalf("source = %q, want %q", result.Source, CookieSourceEnv)
	}
	if cookies := c.GetCookies(); cookies.Secure1PSIDTS != testCookieTS {
		t.Fatalf("psidts = %q, want unchanged %q", cookies.Secure1PSIDTS, testCookieTS)
	}
}

func TestUpdateCookiesSwapsPairOnlyAfterValidation(t *testing.T) {
	var checkedHeader string
	c := newCookieTestClient(t, func(_ context.Context, header string) error {
		checkedHeader = header
		return nil
	})

	const newPSID = "new-psid-value"
	const newTS = "new-ts-value"
	header := "NID=extra; __Secure-1PSID=" + newPSID + "; __Secure-1PSIDTS=" + newTS

	result, err := c.UpdateCookies(context.Background(), header)
	if err != nil {
		t.Fatalf("UpdateCookies returned error: %v", err)
	}
	if result.Status != CookieUpdateApplied {
		t.Fatalf("status = %q, want %q", result.Status, CookieUpdateApplied)
	}
	if checkedHeader != header {
		t.Fatalf("validated header = %q, want the pushed header %q", checkedHeader, header)
	}

	cookies := c.GetCookies()
	if cookies.Secure1PSID != newPSID || cookies.Secure1PSIDTS != newTS {
		t.Fatalf("cookies = %+v, want the pushed pair", cookies)
	}
	if cookies.Source != CookieSourceAdminAPI {
		t.Fatalf("source = %q, want %q", cookies.Source, CookieSourceAdminAPI)
	}

	persisted, err := os.ReadFile(cookieCacheFile(newPSID))
	if err != nil {
		t.Fatalf("reading cache: %v", err)
	}
	if string(persisted) != newTS {
		t.Fatalf("cache = %q, want %q", string(persisted), newTS)
	}

	c.mu.RLock()
	requestHeader := c.cookieHeader
	c.mu.RUnlock()
	if !strings.Contains(requestHeader, "__Secure-1PSID="+newPSID) {
		t.Fatalf("request cookie header = %q, want it rebuilt from the pushed cookies", requestHeader)
	}
}

func TestUpdateCookiesRejectionLeavesSessionUntouched(t *testing.T) {
	c := newCookieTestClient(t, func(context.Context, string) error {
		return errors.New("authentication failed: SNlM0e not found")
	})

	_, err := c.UpdateCookies(context.Background(),
		"__Secure-1PSID=new-psid; __Secure-1PSIDTS=new-ts")
	var invalid *CookieValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want *CookieValidationError", err)
	}
	if invalid.Reason == "" || strings.Contains(invalid.Reason, "new-") {
		t.Fatalf("reason = %q, want a short reason without cookie material", invalid.Reason)
	}

	cookies := c.GetCookies()
	if cookies.Secure1PSID != testCookiePSID || cookies.Secure1PSIDTS != testCookieTS {
		t.Fatalf("cookies = %+v, want the original pair untouched", cookies)
	}
	if _, err := os.Stat(cookieCacheFile("new-psid")); !os.IsNotExist(err) {
		t.Fatalf("cache file written for a rejected session: %v", err)
	}
}

func TestUpdateCookiesNetworkFailureIsRetryable(t *testing.T) {
	c := newCookieTestClient(t, func(context.Context, string) error {
		return errors.New("failed to reach gemini app: dial tcp: timeout")
	})

	_, err := c.UpdateCookies(context.Background(),
		"__Secure-1PSID=new-psid; __Secure-1PSIDTS=new-ts")
	var check *CookieSessionCheckError
	if !errors.As(err, &check) {
		t.Fatalf("error = %v, want *CookieSessionCheckError", err)
	}
	if cookies := c.GetCookies(); cookies.Secure1PSIDTS != testCookieTS {
		t.Fatalf("psidts = %q, want untouched %q", cookies.Secure1PSIDTS, testCookieTS)
	}
}

func TestCookieStatusFingerprintIsShortAndRedacted(t *testing.T) {
	c := newCookieTestClient(t, nil)

	status := c.CookieStatus()
	sum := sha256.Sum256([]byte(testCookiePSID))
	want := hex.EncodeToString(sum[:])[:cookieFingerprintLen]

	if status.Fingerprint != want {
		t.Fatalf("fingerprint = %q, want %q", status.Fingerprint, want)
	}
	if len(status.Fingerprint) != cookieFingerprintLen {
		t.Fatalf("fingerprint length = %d, want %d", len(status.Fingerprint), cookieFingerprintLen)
	}
	if strings.Contains(status.Fingerprint, testCookiePSID) {
		t.Fatal("fingerprint leaks the cookie value")
	}
	if status.Source != CookieSourceEnv {
		t.Fatalf("source = %q, want %q", status.Source, CookieSourceEnv)
	}
	if status.UpdatedAt.IsZero() || status.CacheAge < 0 {
		t.Fatalf("status = %+v, want a populated update time and non-negative age", status)
	}
}
