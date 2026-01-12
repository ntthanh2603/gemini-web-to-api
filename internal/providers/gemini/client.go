package gemini

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"ai-bridges/internal/config"
	"ai-bridges/internal/providers"

	"github.com/imroc/req/v3"
	"go.uber.org/zap"
)

type Client struct {
	httpClient *req.Client
	cookies    *CookieStore
	at         string 
	mu         sync.RWMutex
	healthy    bool
	log        *zap.Logger
	
	autoRefresh     bool
	refreshInterval time.Duration
	stopRefresh     chan struct{}

	reqMu sync.Mutex
}

type CookieStore struct {
	Secure1PSID   string    `json:"__Secure-1PSID"`
	Secure1PSIDTS string    `json:"__Secure-1PSIDTS"`
	Secure1PSIDCC string    `json:"__Secure-1PSIDCC"`
	UpdatedAt     time.Time `json:"updated_at"`
	mu            sync.RWMutex
}

const (
	defaultRefreshIntervalMinutes = 30
)

func NewClient(cfg *config.Config, log *zap.Logger) *Client {
	cookies := &CookieStore{
		Secure1PSID:   cfg.Gemini.Secure1PSID,
		Secure1PSIDTS: cfg.Gemini.Secure1PSIDTS,
		Secure1PSIDCC: cfg.Gemini.Secure1PSIDCC,
		UpdatedAt:     time.Now(),
	}

	client := req.NewClient().
		SetTimeout(2 * time.Minute).
		SetCommonHeaders(DefaultHeaders)

	refreshIntervalMinutes := cfg.Gemini.RefreshInterval
	if refreshIntervalMinutes <= 0 {
		refreshIntervalMinutes = defaultRefreshIntervalMinutes
	}

	return &Client{
		httpClient:      client,
		cookies:         cookies,
		autoRefresh:     true,
		refreshInterval: time.Duration(refreshIntervalMinutes) * time.Minute,
		stopRefresh:     make(chan struct{}),
		log:             log,
	}
}

func (c *Client) Init(ctx context.Context) error {
	// Clean cookies
	c.cookies.Secure1PSID = cleanCookie(c.cookies.Secure1PSID)
	configPSIDTS := cleanCookie(c.cookies.Secure1PSIDTS) // Save original config value
	c.cookies.Secure1PSIDTS = configPSIDTS
	c.cookies.Secure1PSIDCC = cleanCookie(c.cookies.Secure1PSIDCC)

	// Check if we should use cached cookies or clear cache
	if c.cookies.Secure1PSID != "" {
		cachedTS, err := c.LoadCachedCookies()
		
		// If config has a new PSIDTS that differs from cache, clear cache and use config
		if configPSIDTS != "" && cachedTS != "" && configPSIDTS != cachedTS {
			c.log.Info("Config has new __Secure-1PSIDTS, clearing old cache")
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
	err := c.refreshSessionToken()
	if err != nil {
		c.log.Debug("Initial session token fetch failed, attempting cookie rotation", zap.Error(err))
		// Try to rotate cookies and retry
		if rotErr := c.RotateCookies(); rotErr == nil {
			c.log.Debug("Cookie rotation succeeded, retrying session token fetch")
			err = c.refreshSessionToken()
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

func (c *Client) refreshSessionToken() error {
	// 1. Initial hit to google.com to get extra cookies (NID, etc)
	tmpClient := req.NewClient().
		SetTimeout(30 * time.Second).
		SetUserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	
	resp1, err := tmpClient.R().Get("https://www.google.com/")
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

	// 2. Prepare full cookie string
	cookieStr := fmt.Sprintf("%s__Secure-1PSID=%s; __Secure-1PSIDTS=%s", 
		extraCookies, c.cookies.Secure1PSID, c.cookies.Secure1PSIDTS)
	if c.cookies.Secure1PSIDCC != "" {
		cookieStr += fmt.Sprintf("; __Secure-1PSIDCC=%s", c.cookies.Secure1PSIDCC)
	}

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

	req1, _ := http.NewRequest("GET", "https://gemini.google.com/?hl=en", nil)
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
	req2, _ := http.NewRequest("GET", EndpointInit+"?hl=en", nil)
	for k, v := range commonHeaders {
		req2.Header.Set(k, v)
	}
	req2.Header.Set("Sec-Fetch-Site", "same-origin")
	req2.Header.Set("Cookie", cookieStr)
	req2.Header.Set("Referer", "https://gemini.google.com/")
	req2.Header.Set("Accept-Encoding", "gzip, deflate, br")

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


	re := regexp.MustCompile(`"SNlM0e":"([^"]+)"`)
	matches := re.FindStringSubmatch(body)
	if len(matches) < 2 {
		reFallback := regexp.MustCompile(`\["SNlM0e","([^"]+)"\]`)
		matches = reFallback.FindStringSubmatch(body)
		if len(matches) < 2 {


			errMsg := "authentication failed: SNlM0e not found"
			if strings.Contains(body, "Sign in") || strings.Contains(body, "login") {
				errMsg = "authentication failed: cookies invalid. Please provide __Secure-1PSIDTS in addition to __Secure-1PSID"
			}

			// Log as Info to avoid stack trace for expected auth failures
			c.log.Info(errMsg)
			return fmt.Errorf("%s", errMsg)
		}
	}

	c.mu.Lock()
	c.at = matches[1]
	c.healthy = true
	c.mu.Unlock()
	return nil
}

// startAutoRefresh periodically refreshes the PSIDTS cookie
func (c *Client) startAutoRefresh() {
	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := c.RotateCookies(); err != nil {
				c.log.Error("Cookie rotation failed", zap.Error(err))
			}
		case <-c.stopRefresh:
			return
		}
	}
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
	if c.cookies.Secure1PSIDCC != "" {
		parts = append(parts, fmt.Sprintf("__Secure-1PSIDCC=%s", c.cookies.Secure1PSIDCC))
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
		if cookie.Name == "__Secure-1PSIDCC" {
			c.cookies.Secure1PSIDCC = cookie.Value
		}
		// Sync to req/v3 client for future calls
		c.httpClient.SetCommonCookies(cookie)
	}

	if found {
		c.log.Info("Cookie rotated successfully", zap.Time("updated_at", c.cookies.UpdatedAt))
		return nil
	}

	return errors.New("no new __Secure-1PSIDTS cookie received")
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

func (c *Client) GenerateContent(ctx context.Context, prompt string, options ...providers.GenerateOption) (*providers.Response, error) {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()

	config := &providers.GenerateConfig{
		Model: "gemini-pro", // default
	}
	for _, opt := range options {
		opt(config)
	}

	if c.at == "" {
		return nil, errors.New("client not initialized")
	}

	// Build request payload
	inner := []interface{}{
		[]interface{}{prompt},
		nil,
		nil,
	}

	innerJSON, _ := json.Marshal(inner)
	outer := []interface{}{nil, string(innerJSON)}
	outerJSON, _ := json.Marshal(outer)

	formData := map[string]string{
		"at":    c.at,
		"f.req": string(outerJSON),
	}

	resp, err := c.httpClient.R().
		SetFormData(formData).
		SetQueryParam("at", c.at).
		Post(EndpointGenerate)

	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("generate failed with status: %d", resp.StatusCode)
	}

	return c.parseResponse(resp.String())
}

func (c *Client) StartChat(options ...providers.ChatOption) providers.ChatSession {
	config := &providers.ChatConfig{
		Model: "gemini-pro",
	}
	for _, opt := range options {
		opt(config)
	}

	return &ChatSession{
		client:   c,
		model:    config.Model,
		metadata: config.Metadata,
		history:  []providers.Message{},
	}
}

func (c *Client) Close() error {
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

func (c *Client) ListModels() []providers.ModelInfo {
	var models []providers.ModelInfo
	for _, m := range providers.SupportedModels {
		if m.Provider == "gemini" {
			models = append(models, m)
		}
	}
	return models
}

// parseResponse parses Gemini's response format
func (c *Client) parseResponse(text string) (*providers.Response, error) {
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
				if !ok || len(itemArray) < 3 {
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

				if len(payload) > 4 {
					candidates, ok := payload[4].([]interface{})
					if ok && candidates != nil && len(candidates) > 0 {
						firstCandidate, ok := candidates[0].([]interface{})
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

									return &providers.Response{
										Text: resText,
										Metadata: map[string]any{
											"cid":  cid,
											"rid":  rid,
											"rcid": rcid,
										},
									}, nil
								}
							}
						}
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("failed to parse response")
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
	if cs.Secure1PSIDCC != "" {
		cookies = append(cookies, &http.Cookie{
			Name:     "__Secure-1PSIDCC",
			Value:    cleanCookie(cs.Secure1PSIDCC),
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
	
	c.log.Debug("Cleared cookie cache", zap.String("file", filename))
	return nil
}
