package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imroc/req/v3"
	"go.uber.org/zap"
)

// Sources recorded on CookieStore.Source. They describe where the cookies in
// use came from and never contain secret material.
const (
	CookieSourceEnv      = "env"
	CookieSourceRotation = "rotation"
	CookieSourceAdminAPI = "admin-api"
)

// cookieFingerprintLen is the number of hex characters of the __Secure-1PSID
// SHA-256 exposed by the status endpoint. It is long enough to correlate
// deployments and too short to recover the cookie.
const cookieFingerprintLen = 12

// CookieUpdateStatus describes the outcome of UpdateCookies.
type CookieUpdateStatus string

const (
	// CookieUpdateApplied means the pushed cookies passed validation and are
	// now in use.
	CookieUpdateApplied CookieUpdateStatus = "updated"
	// CookieUpdateNoop means the pushed __Secure-1PSIDTS is already in use, so
	// nothing was validated or changed.
	CookieUpdateNoop CookieUpdateStatus = "noop"
)

// CookieUpdateResult reports what UpdateCookies did.
type CookieUpdateResult struct {
	Status    CookieUpdateStatus
	UpdatedAt time.Time
	Source    string
}

// CookieValidationError reports a pushed cookie set that was rejected. The
// reason is short and contains no cookie material.
type CookieValidationError struct {
	Reason string
}

func (e *CookieValidationError) Error() string { return e.Reason }

// CookieSessionCheckError reports that the pushed cookies could not be
// verified against Google (network failure or an unusable response). It means
// "try again later", not "these cookies are bad".
type CookieSessionCheckError struct {
	Err error
}

func (e *CookieSessionCheckError) Error() string {
	if e.Err == nil {
		return "session check failed"
	}
	return fmt.Sprintf("session check failed: %v", e.Err)
}

func (e *CookieSessionCheckError) Unwrap() error { return e.Err }

// CookieStatus is the redacted view of the live session served by the status
// endpoint. It never carries __Secure-1PSID or __Secure-1PSIDTS.
type CookieStatus struct {
	// Fingerprint is the first cookieFingerprintLen hex characters of the
	// SHA-256 of __Secure-1PSID.
	Fingerprint string
	// CacheAge is how long ago the in-use set was last updated. Every update
	// rewrites the on-disk cache, so this is also the age of the cache entry.
	CacheAge time.Duration
	// UpdatedAt is when the in-use set was last updated.
	UpdatedAt time.Time
	// Source is the origin of the in-use set (env, rotation, admin-api).
	Source string
}

// UpdateCookies validates a complete browser Cookie header and, only once it
// has been accepted by Google, swaps it in as the live session. The swap is
// guarded by the cookie lock so concurrent requests and the rotation loop never
// observe a half-updated pair. Validation failures leave the current session
// untouched.
//
// Pushing the __Secure-1PSIDTS of a healthy live session returns a no-op:
// browser extensions push on every cookie change and that must not turn into a
// burst of Google session checks. An unhealthy session is checked again, so
// re-pushing it can recover it.
//
// The pushed header replaces GEMINI_COOKIES as the base of later session
// refreshes and is saved, so it survives a restart (see loadPushedSession).
func (c *Client) UpdateCookies(ctx context.Context, cookieHeader string) (CookieUpdateResult, error) {
	header := strings.TrimSpace(cookieHeader)
	psid := cleanCookie(cookieHeaderValue(header, "__Secure-1PSID"))
	psidts := cleanCookie(cookieHeaderValue(header, "__Secure-1PSIDTS"))

	// Same requirements as Init: the pair is what the session needs.
	if psid == "" {
		return CookieUpdateResult{}, &CookieValidationError{Reason: "cookies must include __Secure-1PSID"}
	}
	if psidts == "" {
		return CookieUpdateResult{}, &CookieValidationError{Reason: "cookies must include __Secure-1PSIDTS"}
	}

	// One session change at a time: Init, the refresh loop and other pushes
	// wait, so none of them interleaves with this check and swap.
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()

	c.cookies.mu.RLock()
	currentPSIDTS := c.cookies.Secure1PSIDTS
	currentUpdatedAt := c.cookies.UpdatedAt
	currentSource := c.cookies.Source
	c.cookies.mu.RUnlock()

	if psidts == currentPSIDTS && c.IsHealthy() {
		c.log.Debug("Cookie update skipped; __Secure-1PSIDTS is already in use",
			zap.String("source", CookieSourceAdminAPI))
		return CookieUpdateResult{
			Status:    CookieUpdateNoop,
			UpdatedAt: currentUpdatedAt,
			Source:    currentSource,
		}, nil
	}

	// Validate against Google before touching any live state.
	probe, err := c.checkSession(ctx, header, psid, psidts)
	if err != nil {
		return CookieUpdateResult{}, err
	}

	// Validation passed: swap the pair atomically. SaveCachedCookies is called
	// while the write lock is held, mirroring RotateCookies, so the cache and
	// the in-memory pair cannot disagree.
	c.cookies.mu.Lock()
	c.cookies.Secure1PSID = psid
	c.cookies.Secure1PSIDTS = psidts
	c.cookies.UpdatedAt = time.Now()
	c.cookies.Source = CookieSourceAdminAPI
	updatedAt := c.cookies.UpdatedAt
	if saveErr := c.SaveCachedCookies(); saveErr != nil {
		// The session is already live; a cache write failure must not fail the
		// request. The next rotation will retry the write.
		c.log.Warn("Failed to persist updated cookies",
			zap.String("source", CookieSourceAdminAPI),
			zap.Error(saveErr))
	}
	c.cookies.mu.Unlock()

	// Adopt the request-facing session state so in-flight work draining after
	// the swap and all new requests use the validated session. The pushed
	// header also replaces GEMINI_COOKIES as the base of later refreshes, or
	// the next one would bring back the old SID/SAPISID next to the new pair.
	c.mu.Lock()
	c.configuredCookieHeader = header
	if probe != nil {
		c.at = probe.at
		c.pushID = probe.pushID
		c.buildLabel = probe.buildLabel
		c.sessionID = probe.sessionID
		c.language = probe.language
		c.cachedModels = probe.cachedModels
		c.cookieHeader = probe.cookieHeader
	} else {
		c.cookieHeader = mergeCookieHeaders(header,
			fmt.Sprintf("__Secure-1PSID=%s; __Secure-1PSIDTS=%s", psid, psidts))
	}
	c.healthy = true
	c.mu.Unlock()

	c.savePushedSession(header, updatedAt)
	// Keep the pushed session rotated even when Init failed and never started
	// the refresh loop.
	c.ensureAutoRefresh()

	c.log.Info("Gemini session cookies updated",
		zap.String("source", CookieSourceAdminAPI),
		zap.Time("updated_at", updatedAt))

	return CookieUpdateResult{
		Status:    CookieUpdateApplied,
		UpdatedAt: updatedAt,
		Source:    CookieSourceAdminAPI,
	}, nil
}

// CookieStatus returns a redacted snapshot of the live session.
func (c *Client) CookieStatus() CookieStatus {
	c.cookies.mu.RLock()
	defer c.cookies.mu.RUnlock()

	age := time.Duration(0)
	if !c.cookies.UpdatedAt.IsZero() {
		if since := time.Since(c.cookies.UpdatedAt); since > 0 {
			age = since
		}
	}

	return CookieStatus{
		Fingerprint: cookieFingerprint(c.cookies.Secure1PSID),
		CacheAge:    age,
		UpdatedAt:   c.cookies.UpdatedAt,
		Source:      c.cookies.Source,
	}
}

// checkSession runs the same live Google check Init performs. On success it
// returns a detached client holding the freshly discovered session state, or
// nil when the check is provided by a test seam.
func (c *Client) checkSession(ctx context.Context, header, psid, psidts string) (*Client, error) {
	if c.sessionValidator != nil {
		if err := c.sessionValidator(ctx, header); err != nil {
			return nil, classifySessionError(err)
		}
		return nil, nil
	}

	probe := &Client{
		httpClient:             req.NewClient().SetTimeout(10 * time.Minute).SetCommonHeaders(DefaultHeaders),
		cookies:                &CookieStore{Secure1PSID: psid, Secure1PSIDTS: psidts, UpdatedAt: time.Now()},
		configuredCookieHeader: header,
		authUser:               c.authUser,
		log:                    c.log,
	}
	if err := probe.refreshSessionToken(ctx); err != nil {
		return nil, classifySessionError(err)
	}
	return probe, nil
}

// classifySessionError separates "these cookies are bad" (4xx) from "Google
// could not be reached" (5xx). Rejection details are never echoed back to the
// caller, so no upstream payload can leak cookie material.
func classifySessionError(err error) error {
	var invalid *CookieValidationError
	var check *CookieSessionCheckError
	if errors.As(err, &invalid) || errors.As(err, &check) {
		return err
	}

	msg := err.Error()
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(msg, "failed to reach gemini app") ||
		strings.Contains(msg, "failed to discover Gemini models") {
		return &CookieSessionCheckError{Err: err}
	}
	return &CookieValidationError{Reason: "cookies rejected by Google"}
}

// cookieHeaderValue extracts a cookie value from a Cookie header string.
func cookieHeaderValue(header, wanted string) string {
	for _, pair := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.TrimSpace(name) == wanted {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// cookieFingerprint returns the short, non-reversible identifier of a PSID.
func cookieFingerprint(psid string) string {
	if psid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(psid))
	return hex.EncodeToString(sum[:])[:cookieFingerprintLen]
}

// pushedSessionFile keeps the last session pushed through UpdateCookies, next
// to the __Secure-1PSIDTS cache, so it survives a restart.
var pushedSessionFile = filepath.Join(".cookies", "pushed-session.json")

// pushedSession is the on-disk form of a pushed session. EnvHash is the
// cookieHeaderHash of GEMINI_COOKIES when the session was pushed.
type pushedSession struct {
	CookieHeader string    `json:"cookie_header"`
	EnvHash      string    `json:"env_hash"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// cookieHeaderHash fingerprints a GEMINI_COOKIES value without storing it.
func cookieHeaderHash(header string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(header)))
	return hex.EncodeToString(sum[:])
}

// savePushedSession writes a pushed session to disk. A failure is logged, not
// returned: the session is already live, only a restart would lose it.
func (c *Client) savePushedSession(header string, updatedAt time.Time) {
	data, err := json.Marshal(pushedSession{CookieHeader: header, EnvHash: c.envCookieHash, UpdatedAt: updatedAt})
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(pushedSessionFile), 0o755); err == nil {
			err = os.WriteFile(pushedSessionFile, data, 0o600)
		}
	}
	if err != nil {
		c.log.Warn("Failed to save the pushed session; a restart will use GEMINI_COOKIES",
			zap.String("source", CookieSourceAdminAPI), zap.Error(err))
	}
}

// loadPushedSession returns the session last pushed through UpdateCookies,
// provided GEMINI_COOKIES is unchanged since that push. An edited
// GEMINI_COOKIES means the operator picked a session after the push, so it
// wins and the pushed one is ignored.
func (c *Client) loadPushedSession() (pushedSession, bool) {
	data, err := os.ReadFile(pushedSessionFile)
	if err != nil {
		return pushedSession{}, false
	}
	var pushed pushedSession
	if err := json.Unmarshal(data, &pushed); err != nil || pushed.EnvHash != c.envCookieHash {
		return pushedSession{}, false
	}
	if cookieHeaderValue(pushed.CookieHeader, "__Secure-1PSID") == "" ||
		cookieHeaderValue(pushed.CookieHeader, "__Secure-1PSIDTS") == "" {
		return pushedSession{}, false
	}
	return pushed, true
}

// usePushedSession makes a saved pushed session the starting point of Init in
// place of GEMINI_COOKIES.
func (c *Client) usePushedSession(pushed pushedSession) {
	c.mu.Lock()
	c.configuredCookieHeader = pushed.CookieHeader
	c.mu.Unlock()

	c.cookies.mu.Lock()
	c.cookies.Secure1PSID = cleanCookie(cookieHeaderValue(pushed.CookieHeader, "__Secure-1PSID"))
	c.cookies.Secure1PSIDTS = cleanCookie(cookieHeaderValue(pushed.CookieHeader, "__Secure-1PSIDTS"))
	c.cookies.UpdatedAt = pushed.UpdatedAt
	c.cookies.Source = CookieSourceAdminAPI
	c.cookies.mu.Unlock()

	c.log.Info("Using the session pushed through /admin/cookies; GEMINI_COOKIES is unchanged since that push",
		zap.Time("pushed_at", pushed.UpdatedAt))
}
