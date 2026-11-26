package providers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Google keeps a Gemini session alive by handing out new cookie values in
// Set-Cookie headers (SIDCC, __Secure-1PSIDCC, sometimes __Secure-1PSIDTS).
// Once a newer value has been used, the older one can be rejected, so the
// cookies a session started with stop working after a while even though the
// running process is still fine. The helpers below keep the live Cookie header
// current and write it to pushedSessionFile, so a restart resumes the session
// the process was actually using instead of the one it started with.

// parseCookieHeader splits a Cookie header into its cookie names, in order of
// first appearance, and their values (the last one wins for a repeated name).
func parseCookieHeader(header string) ([]string, map[string]string) {
	names := make([]string, 0)
	values := make(map[string]string)
	for _, pair := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		if _, exists := values[name]; !exists {
			names = append(names, name)
		}
		values[name] = strings.TrimSpace(value)
	}
	return names, values
}

// applySetCookies returns header with the cookies of a response applied:
// a new value replaces the old one in place, a new cookie is appended and a
// deleted cookie (Max-Age <= 0 or an expiry in the past) is removed. changed
// reports whether the result differs from header.
func applySetCookies(header string, cookies []*http.Cookie) (string, bool) {
	names, values := parseCookieHeader(header)

	changed := false
	now := time.Now()
	for _, ck := range cookies {
		if ck == nil || ck.Name == "" {
			continue
		}
		old, exists := values[ck.Name]
		deleted := ck.MaxAge < 0 || (!ck.Expires.IsZero() && ck.Expires.Before(now))
		switch {
		case deleted:
			if exists {
				delete(values, ck.Name)
				changed = true
			}
		case !exists:
			names = append(names, ck.Name)
			values[ck.Name] = ck.Value
			changed = true
		case old != ck.Value:
			values[ck.Name] = ck.Value
			changed = true
		}
	}

	parts := make([]string, 0, len(names))
	for _, name := range names {
		if value, ok := values[name]; ok {
			parts = append(parts, name+"="+value)
		}
	}
	return strings.Join(parts, "; "), changed
}

// cookieHeaderChanges returns the cookies that turn header before into header
// after, in the form applySetCookies takes: changed and new cookies with their
// new value, removed cookies as deletions.
func cookieHeaderChanges(before, after string) []*http.Cookie {
	beforeNames, beforeValues := parseCookieHeader(before)
	afterNames, afterValues := parseCookieHeader(after)

	changes := make([]*http.Cookie, 0)
	for _, name := range afterNames {
		if old, ok := beforeValues[name]; !ok || old != afterValues[name] {
			changes = append(changes, &http.Cookie{Name: name, Value: afterValues[name]})
		}
	}
	for _, name := range beforeNames {
		if _, ok := afterValues[name]; !ok {
			changes = append(changes, &http.Cookie{Name: name, MaxAge: -1})
		}
	}
	return changes
}

// adoptResponseCookies applies the cookies Google set on a response to a
// request that was sent with sentHeader. They are only applied while that
// request's session (__Secure-1PSID) is still the live one, so a response that
// arrives after a cookie push cannot mix the old session into the new one.
func (c *Client) adoptResponseCookies(sentHeader string, cookies []*http.Cookie) {
	if len(cookies) == 0 {
		return
	}
	sentPSID := cookieHeaderValue(sentHeader, "__Secure-1PSID")

	c.mu.Lock()
	if sentPSID == "" || cookieHeaderValue(c.cookieHeader, "__Secure-1PSID") != sentPSID {
		c.mu.Unlock()
		return
	}
	header, changed := applySetCookies(c.cookieHeader, cookies)
	if changed {
		c.cookieHeader = header
		c.configuredCookieHeader = header
	}
	c.mu.Unlock()
	if !changed {
		return
	}

	c.syncCookiePair(header)
	c.saveLiveSession()
}

// syncCookiePair moves a __Secure-1PSIDTS that Google issued through
// Set-Cookie into the cookie store, so the next refresh sends it instead of
// overriding it with the value the session started with.
func (c *Client) syncCookiePair(header string) {
	psid := cleanCookie(cookieHeaderValue(header, "__Secure-1PSID"))
	psidts := cleanCookie(cookieHeaderValue(header, "__Secure-1PSIDTS"))

	c.cookies.mu.Lock()
	defer c.cookies.mu.Unlock()
	if psidts == "" || psid != c.cookies.Secure1PSID || psidts == c.cookies.Secure1PSIDTS {
		return
	}
	c.cookies.Secure1PSIDTS = psidts
	c.cookies.UpdatedAt = time.Now()
	c.cookies.Source = CookieSourceRotation
	_ = c.SaveCachedCookies()
	c.log.Info("Google issued a new __Secure-1PSIDTS for the live session",
		zap.Time("updated_at", c.cookies.UpdatedAt))
}

// saveLiveSession writes the Cookie header in use to pushedSessionFile, so a
// restart resumes it. A header without the session pair is never written.
func (c *Client) saveLiveSession() {
	c.mu.RLock()
	header := c.cookieHeader
	c.mu.RUnlock()
	if cookieHeaderValue(header, "__Secure-1PSID") == "" ||
		cookieHeaderValue(header, "__Secure-1PSIDTS") == "" {
		return
	}

	c.cookies.mu.RLock()
	saved := pushedSession{
		CookieHeader: header,
		EnvHash:      c.envCookieHash,
		UpdatedAt:    c.cookies.UpdatedAt,
		Source:       c.cookies.Source,
	}
	c.cookies.mu.RUnlock()
	c.writeSessionFile(saved)
}

// writeSessionFile replaces pushedSessionFile atomically. Writes are
// serialized and skipped when the content is unchanged. A failure is logged,
// not returned: the session is live, only a restart would lose it.
func (c *Client) writeSessionFile(saved pushedSession) {
	data, err := json.Marshal(saved)
	if err != nil {
		c.log.Warn("Failed to encode the live session", zap.Error(err))
		return
	}

	c.sessionFileMu.Lock()
	defer c.sessionFileMu.Unlock()
	if string(data) == c.lastSavedSession {
		return
	}

	dir := filepath.Dir(pushedSessionFile)
	if err = os.MkdirAll(dir, 0o755); err == nil {
		var tmp *os.File
		if tmp, err = os.CreateTemp(dir, ".session-*.tmp"); err == nil {
			_, err = tmp.Write(data)
			if closeErr := tmp.Close(); err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Chmod(tmp.Name(), 0o600)
			}
			if err == nil {
				err = os.Rename(tmp.Name(), pushedSessionFile)
			}
			if err != nil {
				_ = os.Remove(tmp.Name())
			}
		}
	}
	if err != nil {
		c.log.Warn("Failed to save the live session; a restart will start from older cookies",
			zap.String("file", pushedSessionFile), zap.Error(err))
		return
	}
	c.lastSavedSession = string(data)
}
