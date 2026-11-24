package providers

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const testEnvCookies = "NID=extra; __Secure-1PSID=" + testCookiePSID + "; __Secure-1PSIDTS=" + testCookieTS

func TestApplySetCookies(t *testing.T) {
	header := "A=1; SIDCC=old; B=2; GONE=x; STALE=y"
	got, changed := applySetCookies(header, []*http.Cookie{
		{Name: "SIDCC", Value: "new"},
		{Name: "C", Value: "3"},
		{Name: "GONE", Value: "", MaxAge: -1},
		{Name: "STALE", Value: "", Expires: time.Now().Add(-time.Hour)},
	})
	if want := "A=1; SIDCC=new; B=2; C=3"; got != want || !changed {
		t.Fatalf("applySetCookies = %q, %v; want %q, true", got, changed, want)
	}

	same, changed := applySetCookies(got, []*http.Cookie{{Name: "A", Value: "1"}, {Name: "MISSING", MaxAge: -1}})
	if same != got || changed {
		t.Fatalf("unchanged cookies reported a change: %q, %v", same, changed)
	}
}

// The cookies Google sets on answers must be what a restart resumes, not the
// cookies the session was pushed with.
func TestResponseCookiesSurviveRestart(t *testing.T) {
	c := newCookieTestClient(t, func(context.Context, string) error { return nil })
	pushed := "SID=new-sid; SIDCC=pushed-cc; __Secure-1PSID=new-psid; __Secure-1PSIDTS=new-ts"
	if _, err := c.UpdateCookies(context.Background(), pushed); err != nil {
		t.Fatal(err)
	}

	c.mu.RLock()
	sent := c.cookieHeader
	c.mu.RUnlock()
	c.adoptResponseCookies(sent, []*http.Cookie{
		{Name: "SIDCC", Value: "answer-cc"},
		{Name: "__Secure-1PSIDTS", Value: "google-ts"},
	})

	if got := c.GetCookies(); got.Secure1PSIDTS != "google-ts" || got.Source != CookieSourceRotation {
		t.Fatalf("live cookies = %+v, want the PSIDTS Google issued", got)
	}
	if cached, err := c.LoadCachedCookies(); err != nil || cached != "google-ts" {
		t.Fatalf("PSIDTS cache = %q (%v), want google-ts", cached, err)
	}

	restarted := restartedClient(testEnvCookies)
	if err := restarted.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	cookies := restarted.GetCookies()
	if cookies.Secure1PSID != "new-psid" || cookies.Secure1PSIDTS != "google-ts" || cookies.Source != CookieSourceRotation {
		t.Fatalf("cookies after restart = %+v, want the live session", cookies)
	}
	restarted.mu.RLock()
	configured := restarted.configuredCookieHeader
	restarted.mu.RUnlock()
	want := "SID=new-sid; SIDCC=answer-cc; __Secure-1PSID=new-psid; __Secure-1PSIDTS=google-ts"
	if configured != want {
		t.Fatalf("header after restart = %q, want %q", configured, want)
	}
}

// An answer to a request sent before a cookie push must not leak the old
// session's cookies into the pushed one.
func TestResponseCookiesFromReplacedSessionIgnored(t *testing.T) {
	c := newCookieTestClient(t, func(context.Context, string) error { return nil })
	if _, err := c.UpdateCookies(context.Background(), "SIDCC=pushed-cc; __Secure-1PSID=new-psid; __Secure-1PSIDTS=new-ts"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pushedSessionFile)
	if err != nil {
		t.Fatal(err)
	}

	c.adoptResponseCookies(testEnvCookies, []*http.Cookie{{Name: "SIDCC", Value: "old-session-cc"}})

	c.mu.RLock()
	header := c.cookieHeader
	c.mu.RUnlock()
	if strings.Contains(header, "old-session-cc") {
		t.Fatalf("live header %q took a cookie from the replaced session", header)
	}
	after, err := os.ReadFile(pushedSessionFile)
	if err != nil || string(after) != string(before) {
		t.Fatalf("session file changed by a stale answer (err %v)", err)
	}
}

// A session that started from GEMINI_COOKIES, never pushed, is also saved
// after a successful refresh and resumed by a restart.
func TestRefreshedEnvSessionSurvivesRestart(t *testing.T) {
	c := newCookieTestClient(t, nil)
	c.autoRefresh = false
	c.refreshSessionTokenFn = func(context.Context) error {
		c.mu.Lock()
		c.at = "token"
		c.cookieHeader = testEnvCookies + "; SIDCC=refreshed-cc"
		c.configuredCookieHeader = c.cookieHeader
		c.mu.Unlock()
		return nil
	}
	if err := c.Init(context.Background()); err != nil {
		t.Fatal(err)
	}

	restarted := restartedClient(testEnvCookies)
	if err := restarted.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted.mu.RLock()
	configured := restarted.configuredCookieHeader
	restarted.mu.RUnlock()
	if !strings.Contains(configured, "SIDCC=refreshed-cc") {
		t.Fatalf("header after restart = %q, want the refreshed session", configured)
	}
	if got := restarted.GetCookies(); got.Source != CookieSourceEnv {
		t.Fatalf("source after restart = %q, want %q", got.Source, CookieSourceEnv)
	}

	info, err := os.Stat(pushedSessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("session file mode = %o, want 600", mode)
	}
}
