package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
// Pushing the __Secure-1PSIDTS that is already in use returns a no-op: browser
// extensions push on every cookie change and that must not turn into a burst of
// Google session checks.
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

	c.cookies.mu.RLock()
	currentPSIDTS := c.cookies.Secure1PSIDTS
	currentUpdatedAt := c.cookies.UpdatedAt
	currentSource := c.cookies.Source
	c.cookies.mu.RUnlock()

	if psidts == currentPSIDTS {
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
	// the swap and all new requests use the validated session.
	c.mu.Lock()
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
