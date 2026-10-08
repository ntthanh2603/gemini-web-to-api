package providers

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gemini-web-to-api/internal/commons/configs"
	"gemini-web-to-api/internal/commons/utils"

	"github.com/google/uuid"
	"github.com/imroc/req/v3"
	"go.uber.org/zap"
)

type Client struct {
	httpClient             *req.Client
	cookies                *CookieStore
	configuredCookieHeader string // optional full browser Cookie header for rollout/session flags
	authUser               string // Google multi-login slot used by the Gemini Web tab (for example "2")
	at                     string
	cookieHeader           string // full Cookie header string built by refreshSessionToken, used in GenerateContent
	pushID                 string
	buildLabel             string
	sessionID              string
	language               string
	generationID           string       // client session UUID used in model-selection headers
	mu                     sync.RWMutex // protects session fields and cachedModels
	healthy                bool
	log                    *zap.Logger

	autoRefresh      bool
	refreshInterval  time.Duration
	stopRefresh      chan struct{}
	maxRetries       int
	cachedModels     []geminiModel
	defaultTemporary bool

	videoJobsOnce sync.Once
	videoJobs     *videoJobStore
}

type CookieStore struct {
	Secure1PSID   string    `json:"__Secure-1PSID"`
	Secure1PSIDTS string    `json:"__Secure-1PSIDTS"`
	UpdatedAt     time.Time `json:"updated_at"`
	mu            sync.RWMutex
}

const (
	defaultRefreshIntervalMinutes = 30
)

var (
	accessTokenRegex         = regexp.MustCompile(`"SNlM0e":"([^"]+)"`)
	accessTokenFallbackRegex = regexp.MustCompile(`\["SNlM0e","([^"]+)"\]`)
	pushIDRegex              = regexp.MustCompile(`"qKIAYe":"([^"]+)"`)
	buildLabelRegex          = regexp.MustCompile(`"cfb2h":"([^"]+)"`)
	sessionIDRegex           = regexp.MustCompile(`"FdrFJe":"([^"]+)"`)
	languageRegex            = regexp.MustCompile(`"TuX5cc":"([^"]+)"`)
	imageURLRegex            = regexp.MustCompile(`(?i)(?:https?:)?//[^\s"'<>\\]+|(?:[a-z0-9.-]+\.)?googleusercontent\.com/[^\s"'<>\\]+`)
)

func NewClient(cfg *configs.Config, log *zap.Logger) *Client {
	cookies := &CookieStore{
		Secure1PSID:   cfg.Gemini.Secure1PSID,
		Secure1PSIDTS: cfg.Gemini.Secure1PSIDTS,
		UpdatedAt:     time.Now(),
	}

	client := req.NewClient().
		SetTimeout(10 * time.Minute).
		SetCommonHeaders(DefaultHeaders)

	refreshIntervalMinutes := cfg.Gemini.RefreshInterval
	if refreshIntervalMinutes <= 0 {
		refreshIntervalMinutes = defaultRefreshIntervalMinutes
	}

	return &Client{
		httpClient:             client,
		cookies:                cookies,
		configuredCookieHeader: strings.TrimSpace(cfg.Gemini.Cookies),
		authUser:               strings.TrimSpace(cfg.Gemini.AuthUser),
		autoRefresh:            true,
		refreshInterval:        time.Duration(refreshIntervalMinutes) * time.Minute,
		stopRefresh:            make(chan struct{}),
		maxRetries:             cfg.Gemini.MaxRetries,
		log:                    log,
		defaultTemporary:       cfg.Gemini.Temporary,
	}
}

func (c *Client) Init(ctx context.Context) error {
	// Clean cookies
	c.cookies.Secure1PSID = cleanCookie(c.cookies.Secure1PSID)
	configPSIDTS := cleanCookie(c.cookies.Secure1PSIDTS) // Save original config value
	c.cookies.Secure1PSIDTS = configPSIDTS

	// Check if we should use cached cookies or clear cache
	if c.cookies.Secure1PSID != "" {
		cachedTS, err := c.LoadCachedCookies()

		// If config has a new PSIDTS that differs from cache, clear cache and use config
		if configPSIDTS != "" && cachedTS != "" && configPSIDTS != cachedTS {
			_ = c.ClearCookieCache()
			// Keep using the config value (already set above)
		} else if err == nil && cachedTS != "" && configPSIDTS == "" {
			// Only use cache if config doesn't provide PSIDTS
			c.cookies.Secure1PSIDTS = cachedTS
			c.log.Info("Loaded __Secure-1PSIDTS from cache")
		}
	}

	// Obtain PSIDTS via rotation if missing
	if c.cookies.Secure1PSID != "" && c.cookies.Secure1PSIDTS == "" {
		c.log.Info("Only __Secure-1PSID provided, attempting to obtain __Secure-1PSIDTS via rotation...")
		if err := c.RotateCookies(); err != nil {
			c.log.Info("Rotation failed, proceeding with just __Secure-1PSID (might fail)", zap.String("error", err.Error()))
		} else {
			c.log.Info("Successfully obtained __Secure-1PSIDTS via rotation")
		}
	}

	// Populate cookies
	c.httpClient.SetCommonCookies(c.cookies.ToHTTPCookies()...)

	// Get SNlM0e token
	err := c.refreshSessionToken(ctx)
	if err != nil {
		c.log.Debug("Initial session token fetch failed, attempting cookie rotation", zap.Error(err))
		// Try to rotate cookies and retry
		if rotErr := c.RotateCookies(); rotErr == nil {
			c.log.Debug("Cookie rotation succeeded, retrying session token fetch")
			err = c.refreshSessionToken(ctx)
		} else {
			c.log.Debug("Cookie rotation failed", zap.Error(rotErr))
		}
	}

	if err != nil {
		return err
	}

	// Save the valid cookies to cache immediately after successful init
	_ = c.SaveCachedCookies()

	c.log.Info("✅ Gemini client initialized successfully")

	// 5. Start auto-refresh in background
	if c.autoRefresh {
		go c.startAutoRefresh()
	}

	return nil
}

func (c *Client) refreshSessionToken(ctx context.Context) error {
	// 1. Initial hit to google.com to get extra cookies (NID, etc)
	tmpClient := req.NewClient().
		SetTimeout(30 * time.Second).
		SetUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp1, err := tmpClient.R().SetContext(ctx).Get("https://www.google.com/")
	extraCookies := ""
	if err == nil {
		parts := []string{}
		for _, ck := range resp1.Cookies() {
			parts = append(parts, fmt.Sprintf("%s=%s", ck.Name, ck.Value))
			// Also sync to main client
			c.httpClient.SetCommonCookies(ck)
		}
		if len(parts) > 0 {
			extraCookies = strings.Join(parts, "; ") + "; "
		}
	}

	// 2. Prepare the full browser session. GEMINI_COOKIES carries rollout and
	// account-selection cookies that 1PSID/1PSIDTS alone may not reproduce.
	// Explicit auth values win if the same names occur in the full header.
	c.cookies.mu.RLock()
	psid := c.cookies.Secure1PSID
	psidts := c.cookies.Secure1PSIDTS
	c.cookies.mu.RUnlock()
	cookieStr := mergeCookieHeaders(extraCookies, c.configuredCookieHeader,
		fmt.Sprintf("__Secure-1PSID=%s; __Secure-1PSIDTS=%s", psid, psidts))

	commonHeaders := map[string]string{
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"Accept-Language":           "en-US,en;q=0.9",
		"Cache-Control":             "max-age=0",
		"Origin":                    "https://gemini.google.com",
		"Sec-Ch-Ua":                 `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`,
		"Sec-Ch-Ua-Mobile":          "?0",
		"Sec-Ch-Ua-Platform":        `"Windows"`,
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
		"X-Same-Domain":             "1",
		"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	}

	hClient := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil // follow redirects
		},
	}

	// Helper to merge cookies into a map to avoid duplicates
	mergeCookies := func(baseStr string, newCks []*http.Cookie) string {
		m := make(map[string]string)
		for _, part := range strings.Split(baseStr, ";") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			kv := strings.SplitN(p, "=", 2)
			if len(kv) == 2 {
				m[kv[0]] = kv[1]
			}
		}
		for _, ck := range newCks {
			m[ck.Name] = ck.Value
		}
		res := []string{}
		for k, v := range m {
			res = append(res, fmt.Sprintf("%s=%s", k, v))
		}
		return strings.Join(res, "; ")
	}

	req1, _ := http.NewRequestWithContext(ctx, "GET", "https://gemini.google.com/?hl=en", nil)
	for k, v := range commonHeaders {
		req1.Header.Set(k, v)
	}
	req1.Header.Set("Cookie", cookieStr)
	resp1_direct, _ := hClient.Do(req1)
	if resp1_direct != nil {
		cookieStr = mergeCookies(cookieStr, resp1_direct.Cookies())
		for _, ck := range resp1_direct.Cookies() {
			c.httpClient.SetCommonCookies(ck)
		}
		resp1_direct.Body.Close()
	}

	// 2. The main INIT hit
	req2, _ := http.NewRequestWithContext(ctx, "GET", geminiAccountURL(EndpointInit, c.authUser)+"?hl=en", nil)
	for k, v := range commonHeaders {
		req2.Header.Set(k, v)
	}
	req2.Header.Set("Sec-Fetch-Site", "same-origin")
	req2.Header.Set("Cookie", cookieStr)
	req2.Header.Set("Referer", "https://gemini.google.com/")

	resp, err := hClient.Do(req2)
	if err != nil {
		return fmt.Errorf("failed to reach gemini app: %w", err)
	}
	defer resp.Body.Close()

	// Dump for debugging if it fails
	// reqDump, _ := httputil.DumpRequestOut(req2, false)
	// respDump, _ := httputil.DumpResponse(resp, false)

	var bodyReader io.ReadCloser = resp.Body
	if strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err == nil {
			bodyReader = gz
			defer gz.Close()
		}
	}

	bodyBytes, _ := io.ReadAll(bodyReader)
	body := string(bodyBytes)

	// Merge cookies from the init response into cookieStr
	cookieStr = mergeCookies(cookieStr, resp.Cookies())

	matches := accessTokenRegex.FindStringSubmatch(body)
	if len(matches) < 2 {
		matches = accessTokenFallbackRegex.FindStringSubmatch(body)
		if len(matches) < 2 {
			errMsg := "authentication failed: SNlM0e not found"
			if strings.Contains(body, "Sign in") || strings.Contains(body, "login") {
				errMsg = "authentication failed: GEMINI_COOKIES is invalid or expired; copy the complete Cookie header from Gemini Web"
			}
			c.log.Info(errMsg)
			return fmt.Errorf("%s", errMsg)
		}
	}

	pushID := "feeds/mcudyrk2a4khkz"
	if pushMatches := pushIDRegex.FindStringSubmatch(body); len(pushMatches) >= 2 {
		pushID = pushMatches[1]
	}
	buildLabel := ""
	if buildMatches := buildLabelRegex.FindStringSubmatch(body); len(buildMatches) >= 2 {
		buildLabel = buildMatches[1]
	}
	sessionID := ""
	if sessionMatches := sessionIDRegex.FindStringSubmatch(body); len(sessionMatches) >= 2 {
		sessionID = sessionMatches[1]
	}
	language := "en"
	if langMatches := languageRegex.FindStringSubmatch(body); len(langMatches) >= 2 {
		language = langMatches[1]
	}

	c.mu.Lock()
	if c.generationID == "" {
		c.generationID = strings.ToUpper(uuid.NewString())
	}
	generationID := c.generationID
	c.mu.Unlock()
	models, err := fetchGeminiModels(ctx, hClient, matches[1], cookieStr, buildLabel, sessionID, language, generationID, c.authUser)
	if err != nil {
		return fmt.Errorf("failed to discover Gemini models: %w", err)
	}

	c.mu.Lock()
	c.at = matches[1]
	c.cookieHeader = cookieStr // save full cookie string for use in GenerateContent
	c.pushID = pushID
	c.buildLabel = buildLabel
	c.sessionID = sessionID
	c.language = language
	c.cachedModels = models
	c.healthy = true
	c.mu.Unlock()

	c.log.Info("Refreshed available models from Gemini Web", zap.Strings("models", c.ListModelsIDs()))

	return nil
}

// startAutoRefresh periodically refreshes the PSIDTS cookie
func (c *Client) startAutoRefresh() {
	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.log.Debug("Starting scheduled cookie refresh")
			rotateErr, sessionErr, healthy := refreshSessionHealth(
				c.RotateCookies,
				func() error { return c.refreshSessionToken(context.Background()) },
			)

			if rotateErr != nil {
				c.log.Warn("Cookie rotation failed; verified the Gemini session separately", zap.Error(rotateErr))
			}
			if sessionErr != nil {
				c.log.Warn("Gemini session token refresh failed", zap.Error(sessionErr))
			}

			c.mu.Lock()
			c.healthy = healthy
			c.mu.Unlock()

			if healthy {
				c.log.Info("Gemini session refresh completed",
					zap.Bool("cookie_rotated", rotateErr == nil),
					zap.Bool("session_token_refreshed", sessionErr == nil),
				)
			} else {
				c.log.Error("Cookie rotation and session verification both failed; marking client unhealthy",
					zap.NamedError("rotation_error", rotateErr),
					zap.NamedError("session_error", sessionErr),
					zap.String("action", "Visit https://gemini.google.com -> F12 -> Network and copy the complete Cookie request header"),
				)
			}
		case <-c.stopRefresh:
			return
		}
	}
}

func refreshSessionHealth(rotate, refreshToken func() error) (rotateErr, sessionErr error, healthy bool) {
	rotateErr = rotate()
	// RotateCookies can reject a session that the Gemini application still accepts.
	// Always verify against Gemini before deciding that the provider is unhealthy.
	sessionErr = refreshToken()
	return rotateErr, sessionErr, rotateErr == nil || sessionErr == nil
}

func (c *Client) RotateCookies() error {
	c.cookies.mu.Lock()
	defer c.cookies.mu.Unlock()

	// Prepare cookies for rotation request
	// NOTE: We access fields directly instead of using ToHTTPCookies() to avoid recursive locking (deadlock)
	parts := []string{}
	if c.cookies.Secure1PSID != "" {
		parts = append(parts, fmt.Sprintf("__Secure-1PSID=%s", c.cookies.Secure1PSID))
	}
	if c.cookies.Secure1PSIDTS != "" {
		parts = append(parts, fmt.Sprintf("__Secure-1PSIDTS=%s", c.cookies.Secure1PSIDTS))
	}
	cookieStr := strings.Join(parts, "; ")

	// Payload must be exactly this string
	strBody := `[000,"-0000000000000000000"]`
	req, _ := http.NewRequest("POST", EndpointRotateCookies, strings.NewReader(strBody))

	req.Header.Set("Content-Type", "application/json")
	// Google often blocks requests with default Go-http-client User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Cookie", cookieStr)

	c.log.Debug("Sending rotation request", zap.String("url", EndpointRotateCookies))
	hClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := hClient.Do(req)
	if err != nil {
		// Log as Info to avoid scary stacktraces in development mode for expected auth failures
		c.log.Info("Rotation request failed (network/auth issue)", zap.String("error", err.Error()))
		return fmt.Errorf("failed to call rotation endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.log.Info("Rotation failed (likely invalid __Secure-1PSID)", zap.Int("status", resp.StatusCode))
		return fmt.Errorf("rotation failed with status %d", resp.StatusCode)
	}

	// Extract new PSIDTS from Set-Cookie headers
	found := false
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "__Secure-1PSIDTS" {
			c.cookies.Secure1PSIDTS = cookie.Value
			c.cookies.UpdatedAt = time.Now()
			found = true
			// Save the new cookie to cache immediately
			_ = c.SaveCachedCookies()
		}
		// Sync to req/v3 client for future calls
		c.httpClient.SetCommonCookies(cookie)
	}

	if found {
		c.log.Info("Cookie rotated successfully", zap.Time("updated_at", c.cookies.UpdatedAt))
	} else {
		// Google returns 200 but omits a new cookie when the existing one is still valid — not an error
		c.log.Debug("No new __Secure-1PSIDTS issued; existing cookie is still valid")
	}
	return nil
}

func (c *Client) GetCookies() *CookieStore {
	c.cookies.mu.RLock()
	defer c.cookies.mu.RUnlock()

	return &CookieStore{
		Secure1PSID:   c.cookies.Secure1PSID,
		Secure1PSIDTS: c.cookies.Secure1PSIDTS,
		UpdatedAt:     c.cookies.UpdatedAt,
	}
}

func (c *Client) GenerateContent(ctx context.Context, prompt string, options ...GenerateOption) (*Response, error) {
	return c.generateContent(ctx, prompt, nil, options...)
}

// ResolveModel validates a requested public model name and returns the
// canonical model metadata currently advertised by Gemini Web.
func (c *Client) ResolveModel(requested string) (ModelInfo, error) {
	base, canvas := splitCanvasModel(requested)
	base, music := splitMusicModel(base)
	c.mu.RLock()
	model, err := resolveGeminiModel(base, c.cachedModels)
	c.mu.RUnlock()
	if err != nil {
		return ModelInfo{}, err
	}
	info := model.ModelInfo
	if canvas {
		info.ID += CanvasModelSuffix
		if info.DisplayName != "" {
			info.DisplayName += " (Canvas)"
		}
	}
	if music {
		info.ID += MusicModelSuffix
		if info.DisplayName != "" {
			info.DisplayName += " (Music)"
		}
	}
	return info, nil
}

func (c *Client) generateContent(ctx context.Context, prompt string, metadata []interface{}, options ...GenerateOption) (*Response, error) {
	config := &GenerateConfig{}
	for _, opt := range options {
		opt(config)
	}
	if model, canvas := splitCanvasModel(config.Model); canvas {
		config.Model = model
		config.Canvas = true
	}
	if model, music := splitMusicModel(config.Model); music {
		config.Model = model
		if config.Music == nil {
			config.Music = &MusicConfig{}
		}
	}

	c.mu.Lock()
	if c.generationID == "" {
		c.generationID = strings.ToUpper(uuid.NewString())
	}
	selectedModel, modelErr := resolveGeminiModel(config.Model, c.cachedModels)
	at := c.at
	cookieHdr := c.cookieHeader
	buildLabel := c.buildLabel
	sessionID := c.sessionID
	language := c.language
	generationID := c.generationID
	c.mu.Unlock()
	if language == "" {
		language = "en"
	}

	if at == "" {
		return nil, errors.New("client not initialized")
	}
	if modelErr != nil {
		return nil, modelErr
	}
	c.log.Debug("Selected Gemini model", zap.String("requested", config.Model),
		zap.String("model", selectedModel.ID), zap.String("model_id", selectedModel.ModelID),
		zap.Int("capacity", selectedModel.Capacity), zap.Int("capacity_field", selectedModel.CapacityField),
		zap.Int("model_number", selectedModel.ModelNumber))

	uploadedFiles, err := c.uploadRequestFiles(ctx, config, cookieHdr)
	if err != nil {
		return nil, err
	}

	requestID := strings.ToUpper(uuid.NewString())
	inner := buildGenerateInner(prompt, uploadedFiles, selectedModel.ModelNumber, language, requestID, c.defaultTemporary)
	if metadata != nil {
		inner[2] = metadata
	}
	modelHeaders, err := buildGeminiModelHeaders(selectedModel, requestID, generationID)
	if err != nil {
		return nil, err
	}
	if config.Video != nil {
		inner = applyVideoInner(inner, config.Video.AspectRatio)
	} else if config.Music != nil {
		inner = applyMusicInner(inner, *config.Music)
	} else if config.Canvas {
		inner = applyCanvasInner(inner)
	}
	if config.Video != nil || config.Music != nil || config.Canvas {
		if err := applyWebToolModelHeader(modelHeaders); err != nil {
			return nil, err
		}
	}

	innerJSON, _ := json.Marshal(inner)
	outer := []interface{}{nil, string(innerJSON)}
	outerJSON, _ := json.Marshal(outer)

	// Encode form body manually to have full control over the request
	formValues := url.Values{}
	formValues.Set("at", at)
	formValues.Set("f.req", string(outerJSON))
	formBody := formValues.Encode()

	queryValues := url.Values{}
	queryValues.Set("at", at)
	queryValues.Set("hl", language)
	queryValues.Set("_reqid", fmt.Sprintf("%d", rand.Intn(90000)+10000))
	queryValues.Set("rt", "c")
	if buildLabel != "" {
		queryValues.Set("bl", buildLabel)
	}
	if sessionID != "" {
		queryValues.Set("f.sid", sessionID)
	}
	generateURL := geminiAccountURL(EndpointGenerate, c.authUser) + "?" + queryValues.Encode()

	maxAttempts := c.maxRetries
	// A retried video/music request could start (and bill) a second generation.
	if maxAttempts <= 0 || config.Video != nil || config.Music != nil {
		maxAttempts = 1
	}

	// Use a plain http.Client to avoid cookie accumulation issues with the req library
	plainClient := &http.Client{Timeout: 5 * time.Minute}

	totalStart := time.Now()

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			backoff := time.Duration(1<<uint(attempt-2)) * time.Second
			c.log.Warn("Retrying GenerateContent",
				zap.Int("attempt", attempt),
				zap.Int("max_attempts", maxAttempts),
				zap.Duration("backoff", backoff),
				zap.Error(lastErr),
			)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		httpStart := time.Now()

		httpReq, err := http.NewRequestWithContext(ctx, "POST", generateURL, strings.NewReader(formBody))
		if err != nil {
			lastErr = fmt.Errorf("failed to build generate request: %w", err)
			continue
		}
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
		httpReq.Header.Set("Origin", "https://gemini.google.com")
		httpReq.Header.Set("Referer", "https://gemini.google.com/")
		httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		httpReq.Header.Set("X-Same-Domain", "1")
		for name, value := range modelHeaders {
			httpReq.Header.Set(name, value)
		}
		if cookieHdr != "" {
			httpReq.Header.Set("Cookie", cookieHdr)
		}

		httpResp, err := plainClient.Do(httpReq)
		httpDuration := time.Since(httpStart)
		if err != nil {
			c.log.Warn("Generate request failed, will retry",
				zap.Error(err),
				zap.Duration("http_duration", httpDuration),
				zap.Int("attempt", attempt),
			)
			lastErr = err
			continue
		}
		if httpResp.StatusCode != http.StatusOK {
			bodySnippet, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
			_ = httpResp.Body.Close()
			lastErr = fmt.Errorf("generate failed with status: %d", httpResp.StatusCode)
			c.log.Warn("Generate returned non-200",
				zap.Int("status", httpResp.StatusCode),
				zap.String("body_snippet", string(bodySnippet)),
				zap.Int("attempt", attempt),
			)
			if httpResp.StatusCode >= 500 {
				continue
			}
			return nil, lastErr
		}

		respBytes, err := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read generate response: %w", err)
			continue
		}
		respBody := string(respBytes)

		parseStart := time.Now()
		result, parseErr := c.parseResponse(respBody)
		parseDuration := time.Since(parseStart)

		if parseErr != nil {
			lastErr = parseErr
			c.log.Warn("Failed to parse response, will retry",
				zap.Error(parseErr),
				zap.Int("attempt", attempt),
			)
			continue
		}
		result.Model = selectedModel.ID
		result.ModelID = selectedModel.ModelID
		result.RequestedModel = strings.TrimSpace(config.Model)
		if result.RequestedModel == "" {
			result.RequestedModel = selectedModel.ID
		}
		// Gemini may silently serve a lower-tier model when the selected model's
		// web quota is exhausted. A single known model ID in the protocol body is
		// routing evidence; never label that response as the requested model.
		if actual, ok := c.responseModel(respBody); ok && actual.ModelID != selectedModel.ModelID {
			result.Model = actual.ID
			result.ModelID = actual.ModelID
			c.log.Warn("Gemini served a different model",
				zap.String("requested_model", selectedModel.ID),
				zap.String("actual_model", actual.ID),
			)
		}
		if config.Canvas {
			result.Model += CanvasModelSuffix
			result.RequestedModel += CanvasModelSuffix
		}
		c.log.Debug("GenerateContent timing",
			zap.Duration("gemini_server_rtt", httpDuration),
			zap.Duration("parse_duration", parseDuration),
			zap.Duration("total_duration", time.Since(totalStart)),
			zap.Int("attempt", attempt),
			zap.Int("response_bytes", len(respBody)),
		)

		if attempt > 1 {
			c.log.Info("GenerateContent succeeded after retry", zap.Int("attempt", attempt))
		}
		if config.DownloadGeneratedImages {
			for i := range result.Images {
				if !result.Images[i].Generated {
					continue
				}
				encoded, downloadErr := c.downloadGeneratedImage(ctx, result.Images[i].URL, cookieHdr)
				if downloadErr != nil {
					c.log.Warn("Failed to download generated image", zap.Error(downloadErr))
					continue
				}
				result.Images[i].B64JSON = encoded
			}
		}
		return result, nil
	}

	c.log.Error("GenerateContent failed after all attempts",
		zap.Int("attempts", maxAttempts),
		zap.Error(lastErr),
	)
	return nil, fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}

func (c *Client) responseModel(responseBody string) (geminiModel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var found *geminiModel
	for i := range c.cachedModels {
		model := &c.cachedModels[i]
		if model.ModelID == "" || !strings.Contains(responseBody, model.ModelID) {
			continue
		}
		if found != nil {
			return geminiModel{}, false
		}
		copy := *model
		found = &copy
	}
	if found == nil {
		return geminiModel{}, false
	}
	return *found, true
}

const maxGeneratedImageBytes = 50 << 20

func generatedImageHTTPClient(cookieHeader string) *http.Client {
	return &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many generated-image redirects")
			}
			if req.URL.Scheme != "https" || !isTrustedGoogleMediaHost(req.URL.Hostname()) {
				return fmt.Errorf("refusing generated-image redirect to untrusted host")
			}
			if cookieHeader != "" {
				req.Header.Set("Cookie", cookieHeader)
			}
			req.Header.Set("Referer", "https://gemini.google.com/")
			return nil
		},
	}
}

func (c *Client) downloadGeneratedImage(ctx context.Context, rawURL, cookieHeader string) (string, error) {
	imageURL := appendGoogleImageSize(rawURL, 2048)
	parsedImageURL, err := url.Parse(imageURL)
	if err != nil || parsedImageURL.Scheme != "https" || !isTrustedGoogleMediaHost(parsedImageURL.Hostname()) {
		return "", fmt.Errorf("refusing generated-image download from untrusted host")
	}
	client := generatedImageHTTPClient(cookieHeader)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://gemini.google.com/")
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		return "", fmt.Errorf("generated image download returned %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGeneratedImageBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxGeneratedImageBytes {
		return "", fmt.Errorf("generated image exceeds %d byte limit", maxGeneratedImageBytes)
	}
	return base64.StdEncoding.EncodeToString(body), nil
}

// buildGeminiModelHeaders uses the routing identity discovered for this account.
// The ID at index 4 is a model ID, not a randomly generated request/trace ID.
func buildGeminiModelHeaders(model geminiModel, requestID, generationID string) (map[string]string, error) {
	if model.ModelID == "" || model.ModelNumber <= 0 || model.Capacity <= 0 ||
		(model.CapacityField != 12 && model.CapacityField != 13) {
		return nil, fmt.Errorf("model '%s' has incomplete Gemini routing information", model.ID)
	}
	offset := model.CapacityField - 12
	header := make([]interface{}, 17+offset)
	header[0] = 1
	header[4] = model.ModelID
	header[7] = 0
	header[8] = []int{4, 5, 6, 8}
	// CapacityField is a one-based protocol field number.
	header[model.CapacityField-1] = model.Capacity
	header[14+offset] = model.ModelNumber
	header[15+offset] = 1
	header[16+offset] = generationID
	encoded, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	requestHeader, err := json.Marshal([]interface{}{requestID, 1})
	if err != nil {
		return nil, err
	}
	return map[string]string{
		geminiModelHeaderKey:        string(encoded),
		"x-goog-ext-525005358-jspb": string(requestHeader),
		"x-goog-ext-73010989-jspb":  "[0]",
		"x-goog-ext-73010990-jspb":  "[0,0,0]",
	}, nil
}

func buildGenerateInner(prompt string, files []uploadedFile, modelNumber int, language, requestID string, isTemporary bool) []interface{} {
	var messageContent []interface{}
	if len(files) == 0 {
		messageContent = []interface{}{prompt}
	} else {
		fileData := make([]interface{}, 0, len(files))
		for _, file := range files {
			fileData = append(fileData, []interface{}{[]interface{}{file.ID}, file.Name})
		}
		messageContent = []interface{}{prompt, 0, nil, fileData, nil, nil, 0}
	}

	defaultMetadata := []interface{}{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	inner := make([]interface{}, 81)
	inner[0] = messageContent
	inner[1] = []interface{}{language}
	inner[2] = defaultMetadata
	inner[6] = []interface{}{1}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	inner[17] = []interface{}{[]interface{}{0}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []interface{}{4}
	inner[41] = []interface{}{1}
	inner[53] = 0
	inner[59] = requestID
	inner[61] = []interface{}{}
	inner[68] = 1
	inner[79] = modelNumber
	inner[80] = 1

	if isTemporary {
		inner[45] = 1
	}

	return inner
}

func (c *Client) StartChat(options ...ChatOption) ChatSession {
	config := &ChatConfig{}
	for _, opt := range options {
		opt(config)
	}

	// Pin the default chat mode so a registry refresh cannot change it between
	// turns. An explicit Pro request must always go through Pro resolution.
	if config.Model == "" {
		if config.Metadata != nil && config.Metadata.Model != "" {
			config.Model = config.Metadata.Model
		} else {
			c.mu.RLock()
			if len(c.cachedModels) > 0 {
				config.Model = c.cachedModels[0].ID
			}
			c.mu.RUnlock()
		}
	}

	return &GeminiChatSession{
		client:   c,
		model:    config.Model,
		metadata: config.Metadata,
		history:  []Message{},
	}
}

func (c *Client) Close() error {
	c.videoJobStore().close()
	close(c.stopRefresh)
	c.mu.Lock()
	c.healthy = false
	c.mu.Unlock()
	return nil
}

func (c *Client) GetName() string {
	return "gemini"
}

func (c *Client) IsHealthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.healthy
}

func (c *Client) ListModels() []ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.cachedModels) == 0 {
		return []ModelInfo{}
	}

	models := make([]ModelInfo, 0, len(c.cachedModels))
	for _, model := range c.cachedModels {
		models = append(models, model.ModelInfo)
	}
	return models
}

func (c *Client) ListModelsIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ids := make([]string, 0, len(c.cachedModels))
	for _, m := range c.cachedModels {
		ids = append(ids, m.ID)
	}
	return ids
}

// parseResponse parses Gemini's response format
func (c *Client) parseResponse(text string) (*Response, error) {
	var finalResText string
	var finalMetadata map[string]any
	found := false
	imagesByURL := make(map[string]Image)
	var videos []Video
	seenVideos := make(map[string]bool)
	var canvases []Canvas
	var audios []Audio
	seenAudio := make(map[string]bool)
	conversationID := ""

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, ")]}'")

		var root []interface{}
		if err := json.Unmarshal([]byte(line), &root); err == nil {
			for _, item := range root {
				itemArray, ok := item.([]interface{})
				if !ok || len(itemArray) < 1 {
					continue
				}

				// Check for BardErrorInfo error block
				if errStr := extractBardError(itemArray); errStr != "" {
					return nil, errors.New(errStr)
				}

				if len(itemArray) < 3 {
					continue
				}

				payloadStr, ok := itemArray[2].(string)
				if !ok {
					continue
				}

				var payload []interface{}
				if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
					continue
				}
				collectImages(payload, imagesByURL)
				if id := conversationIDFromPayload(payload); id != "" {
					conversationID = id
				}
				if len(payload) > 4 {
					candidates, ok := payload[4].([]interface{})
					if ok && candidates != nil && len(candidates) > 0 {
						for _, rawCandidate := range candidates {
							candidate, ok := rawCandidate.([]interface{})
							if !ok {
								continue
							}
							for _, image := range extractGeneratedImages(candidate) {
								imagesByURL[image.URL] = image
							}
							for _, audio := range extractGeneratedAudio(candidate) {
								if !seenAudio[audio.URL] {
									seenAudio[audio.URL] = true
									audios = append(audios, audio)
								}
							}
							for _, video := range extractGeneratedVideos(candidate) {
								if !seenVideos[video.URL] {
									seenVideos[video.URL] = true
									videos = append(videos, video)
								}
							}
						}
						firstCandidate, ok := candidates[0].([]interface{})
						// Canvas content is streamed in some frames only; keep the latest.
						if found := extractCanvases(firstCandidate); len(found) > 0 {
							canvases = found
						}
						if ok && len(firstCandidate) >= 2 {
							contentParts, ok := firstCandidate[1].([]interface{})
							if ok && len(contentParts) > 0 {
								resText, ok := contentParts[0].(string)
								if ok {
									// Extract conversation metadata if available
									var cid, rid, rcid string
									if len(firstCandidate) > 0 {
										if id, ok := firstCandidate[0].(string); ok {
											rcid = id
										}
									}
									if len(payload) > 1 {
										if id, ok := payload[1].(string); ok {
											cid = id
										}
									}

									finalResText = resText
									finalMetadata = map[string]any{
										"cid":  cid,
										"rid":  rid,
										"rcid": rcid,
									}
									found = true
								}
							}
						}
					}
				}
			}
		}
	}

	if found || len(imagesByURL) > 0 || len(videos) > 0 || len(audios) > 0 {
		images := make([]Image, 0, len(imagesByURL))
		for _, image := range imagesByURL {
			images = append(images, image)
		}
		finalResText = renderCanvasPlaceholders(finalResText, canvases)
		reasoning, cleanText := utils.ExtractThinkingAndText(finalResText)
		return &Response{
			Text:           cleanText,
			ReasoningText:  reasoning,
			Images:         images,
			Videos:         videos,
			Canvases:       canvases,
			Audios:         audios,
			Metadata:       finalMetadata,
			ConversationID: conversationID,
		}, nil
	}

	sample := text
	if len(sample) > 500 {
		sample = sample[:500]
	}
	return nil, fmt.Errorf("failed to parse response. Sample: %s", sample)
}

// extractBardError recursively searches for BardErrorInfo and extracts codes/messages
func extractBardError(item []interface{}) string {
	var codes []int
	var foundError bool

	var tempCodes []int
	var walk func(v any) bool
	walk = func(v any) bool {
		switch val := v.(type) {
		case []interface{}:
			hasError := false
			for _, child := range val {
				if walk(child) {
					hasError = true
				}
			}
			return hasError
		case string:
			return strings.Contains(val, "BardErrorInfo")
		case float64:
			tempCodes = append(tempCodes, int(val))
			return false
		}
		return false
	}

	for _, el := range item {
		tempCodes = nil
		if walk(el) {
			foundError = true
			codes = tempCodes
			break
		}
	}

	if foundError {
		if len(codes) > 0 {
			return fmt.Sprintf("Google Gemini Web returned an error (BardErrorInfo code %v). This usually indicates session expiration, rate limits, context window limits, or bot protection/CAPTCHA block.", codes)
		}
		return "Google Gemini Web returned a BardErrorInfo block."
	}
	return ""
}

func collectImages(value any, out map[string]Image) {
	switch v := value.(type) {
	case []interface{}:
		for _, item := range v {
			collectImages(item, out)
		}
	case map[string]interface{}:
		for _, item := range v {
			collectImages(item, out)
		}
	case string:
		for _, rawURL := range imageURLRegex.FindAllString(v, -1) {
			imageURL := normalizeImageURL(rawURL)
			if imageURL == "" {
				continue
			}
			if _, exists := out[imageURL]; exists {
				continue
			}
			out[imageURL] = Image{
				URL:      imageURL,
				MimeType: mimeTypeFromImageURL(imageURL),
			}
		}
	}
}

// extractGeneratedImages reads the dedicated Gemini Web generated-media slot.
// Current responses store generated images at candidate[12][7][0], where each
// item contains metadata at item[0][3]: filename, URL, MIME and dimensions.
// Generic URL scanning misses some of these URLs because they often have no
// file extension and may be embedded beside non-image googleusercontent URLs.
func extractGeneratedImages(candidate []interface{}) []Image {
	if len(candidate) <= 12 {
		return nil
	}
	candidateMedia, ok := candidate[12].([]interface{})
	if !ok || len(candidateMedia) <= 7 {
		return nil
	}
	mediaGroups, ok := candidateMedia[7].([]interface{})
	if !ok || len(mediaGroups) == 0 {
		return nil
	}
	generated, ok := mediaGroups[0].([]interface{})
	if !ok {
		return nil
	}

	images := make([]Image, 0, len(generated))
	for _, rawImage := range generated {
		imageNode, ok := rawImage.([]interface{})
		if !ok || len(imageNode) == 0 {
			continue
		}
		wrapper, ok := imageNode[0].([]interface{})
		if !ok || len(wrapper) <= 3 {
			continue
		}
		metadata, ok := wrapper[3].([]interface{})
		if !ok || len(metadata) <= 3 {
			continue
		}
		imageURL, ok := metadata[3].(string)
		if !ok || normalizeImageURL(imageURL) == "" {
			continue
		}
		image := Image{URL: normalizeImageURL(imageURL), MimeType: "image/png", Generated: true}
		if filename, ok := metadata[2].(string); ok {
			image.Title = filename
		}
		for _, field := range metadata {
			switch typed := field.(type) {
			case string:
				if strings.HasPrefix(typed, "image/") {
					image.MimeType = typed
				}
			case []interface{}:
				if len(typed) >= 2 {
					if width, ok := typed[0].(float64); ok {
						image.Width = int(width)
					}
					if height, ok := typed[1].(float64); ok {
						image.Height = int(height)
					}
				}
			}
		}
		images = append(images, image)
	}
	return images
}

func appendGoogleImageSize(rawURL string, size int) string {
	if size <= 0 {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	lastSegment := path.Base(parsed.Path)
	if strings.Contains(lastSegment, "=s") || strings.Contains(lastSegment, "=w") {
		return rawURL
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + fmt.Sprintf("=s%d", size)
	return parsed.String()
}

func isTrustedGoogleMediaHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "google.com" || strings.HasSuffix(host, ".google.com") ||
		host == "googleusercontent.com" || strings.HasSuffix(host, ".googleusercontent.com")
}

func normalizeImageURL(rawURL string) string {
	cleaned := html.UnescapeString(strings.TrimSpace(rawURL))
	cleaned = strings.TrimRight(cleaned, ".,);]")
	if strings.HasPrefix(cleaned, "//") {
		cleaned = "https:" + cleaned
	}
	lowerCleaned := strings.ToLower(cleaned)
	if strings.HasPrefix(lowerCleaned, "googleusercontent.com/") || strings.HasSuffix(strings.SplitN(lowerCleaned, "/", 2)[0], ".googleusercontent.com") {
		cleaned = "https://" + cleaned
	}
	parsed, err := url.Parse(cleaned)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if !looksLikeImageURL(parsed) {
		return ""
	}
	if strings.Contains(strings.ToLower(parsed.Hostname()), "googleusercontent.com") {
		parsed.Scheme = "https"
	}
	return parsed.String()
}

func looksLikeImageURL(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	path := strings.ToLower(u.EscapedPath())

	if strings.HasSuffix(path, ".svg") || strings.Contains(host, "fonts.gstatic.com") {
		return false
	}
	if host == "googleusercontent.com" {
		return false
	}
	if strings.HasSuffix(host, ".googleusercontent.com") {
		return true
	}
	switch {
	case strings.HasSuffix(path, ".png"),
		strings.HasSuffix(path, ".jpg"),
		strings.HasSuffix(path, ".jpeg"),
		strings.HasSuffix(path, ".webp"),
		strings.HasSuffix(path, ".gif"):
		return true
	default:
		return false
	}
}

func mimeTypeFromImageURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	path := strings.ToLower(u.EscapedPath())
	switch {
	case strings.HasSuffix(path, ".png"):
		return "image/png"
	case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(path, ".webp"):
		return "image/webp"
	case strings.HasSuffix(path, ".gif"):
		return "image/gif"
	default:
		return ""
	}
}

func (cs *CookieStore) ToHTTPCookies() []*http.Cookie {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	cookies := []*http.Cookie{}
	domain := ".google.com"

	if cs.Secure1PSID != "" {
		cookies = append(cookies, &http.Cookie{
			Name:     "__Secure-1PSID",
			Value:    cleanCookie(cs.Secure1PSID),
			Domain:   domain,
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteNoneMode,
		})
	}
	if cs.Secure1PSIDTS != "" {
		cookies = append(cookies, &http.Cookie{
			Name:     "__Secure-1PSIDTS",
			Value:    cleanCookie(cs.Secure1PSIDTS),
			Domain:   domain,
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteNoneMode,
		})
	}
	return cookies
}

func cleanCookie(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, "\"")
	v = strings.Trim(v, "'")
	v = strings.TrimSuffix(v, ";")
	return v
}

func mergeCookieHeaders(headers ...string) string {
	values := make(map[string]string)
	order := make([]string, 0)
	for _, header := range headers {
		for _, pair := range strings.Split(header, ";") {
			name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
			name = strings.TrimSpace(name)
			if !ok || name == "" {
				continue
			}
			if _, exists := values[name]; !exists {
				order = append(order, name)
			}
			values[name] = strings.TrimSpace(value)
		}
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}

// LoadCachedCookies attempts to read the saved 1PSIDTS from disk
func (c *Client) LoadCachedCookies() (string, error) {
	if c.cookies.Secure1PSID == "" {
		return "", errors.New("no PSID available")
	}

	hash := sha256.Sum256([]byte(c.cookies.Secure1PSID))
	filename := filepath.Join(".cookies", hex.EncodeToString(hash[:])+".txt")

	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}

	ts := strings.TrimSpace(string(data))
	if ts == "" {
		return "", errors.New("empty cache file")
	}
	return ts, nil
}

// SaveCachedCookies writes the current 1PSIDTS to disk
func (c *Client) SaveCachedCookies() error {
	if c.cookies.Secure1PSID == "" || c.cookies.Secure1PSIDTS == "" {
		return nil
	}

	// Create directory if not exists
	if err := os.MkdirAll(".cookies", 0755); err != nil {
		return err
	}

	hash := sha256.Sum256([]byte(c.cookies.Secure1PSID))
	filename := filepath.Join(".cookies", hex.EncodeToString(hash[:])+".txt")

	err := os.WriteFile(filename, []byte(c.cookies.Secure1PSIDTS), 0600)
	if err == nil {
		c.log.Debug("Saved __Secure-1PSIDTS to local cache for future use", zap.String("file", filename))
	} else {
		c.log.Warn("Failed to save cookies to cache", zap.String("file", filename), zap.Error(err))
	}
	return err
}

// ClearCookieCache deletes the cached cookie file for the current PSID
func (c *Client) ClearCookieCache() error {
	if c.cookies.Secure1PSID == "" {
		return nil
	}

	hash := sha256.Sum256([]byte(c.cookies.Secure1PSID))
	filename := filepath.Join(".cookies", hex.EncodeToString(hash[:])+".txt")

	err := os.Remove(filename)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

const (
	EndpointGoogle        = "https://www.google.com"
	EndpointInit          = "https://gemini.google.com/app"
	EndpointGenerate      = "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"
	EndpointRotateCookies = "https://accounts.google.com/RotateCookies"
	EndpointUpload        = "https://content-push.googleapis.com/upload"
	EndpointBatchExec     = "https://gemini.google.com/_/BardChatUi/data/batchexecute"
)

var DefaultHeaders = map[string]string{
	"Content-Type":  "application/x-www-form-urlencoded;charset=utf-8",
	"Origin":        "https://gemini.google.com",
	"Referer":       "https://gemini.google.com/",
	"User-Agent":    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"X-Same-Domain": "1",
}

func geminiAccountURL(endpoint, authUser string) string {
	if authUser == "" {
		return endpoint
	}
	return strings.Replace(endpoint, "https://gemini.google.com/", "https://gemini.google.com/u/"+authUser+"/", 1)
}

func geminiSourcePath(authUser string) string {
	if authUser == "" {
		return "/app"
	}
	return "/u/" + authUser + "/app"
}
