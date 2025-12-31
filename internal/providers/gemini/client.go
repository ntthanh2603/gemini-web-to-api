package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

type Client struct {
	httpClient *req.Client
	cookies    map[string]string
	at         string // SNlM0e
}

func NewClient(secure1PSID, secure1PSIDTS string) *Client {
	cookies := map[string]string{
		"__Secure-1PSID":   secure1PSID,
		"__Secure-1PSIDTS": secure1PSIDTS,
	}

	client := req.NewClient().
		SetTimeout(5 * time.Minute).
		SetCommonHeaders(DefaultHeaders).
		EnableDumpAllWithoutBody() // For debugging

	return &Client{
		httpClient: client,
		cookies:    cookies,
	}
}


func (c *Client) toHttpCookies() []*http.Cookie {
	var cookies []*http.Cookie
	for k, v := range c.cookies {
		cookies = append(cookies, &http.Cookie{
			Name:   k,
			Value:  v,
			Domain: ".google.com",
			Path:   "/",
		})
	}
	return cookies
}

func (c *Client) Init() error {
	// 1. Get Google homepage to set initial cookies (optional but good practice)
	_, err := c.httpClient.R().
		SetCookies(c.toHttpCookies()...).
		Get(EndpointGoogle)
	if err != nil {
		return fmt.Errorf("failed to reach google.com: %w", err)
	}

	// 2. Get Gemini App page to extract SNlM0e
	resp, err := c.httpClient.R().
		SetCookies(c.toHttpCookies()...).
		Get(EndpointInit)
	if err != nil {
		return fmt.Errorf("failed to reach gemini app: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gemini app returned status: %d", resp.StatusCode)
	}

	// Extract SNlM0e
	re := regexp.MustCompile(`"SNlM0e":"(.*?)"`)
	matches := re.FindStringSubmatch(resp.String())
	if len(matches) < 2 {
		return errors.New("SNlM0e not found in response, check cookies")
	}

	c.at = matches[1]
	return nil
}

// GenerateContent sends a message to Gemini and returns the response text.
// This is a simplified version handling single-turn text chat.
func (c *Client) GenerateContent(prompt string) (string, error) {
	if c.at == "" {
		return "", errors.New("client not initialized, call Init() first")
	}

	// Construct the complex payload
	// Inner payload: [["prompt"], null, null]
	inner := []interface{}{
		[]interface{}{prompt},
		nil,
		nil, // chat metadata (cid, rid, rcid)
	}

	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return "", err
	}

	outer := []interface{}{
		nil,
		string(innerJSON),
	}

	outerJSON, err := json.Marshal(outer)
	if err != nil {
		return "", err
	}

	// Request data
	formData := map[string]string{
		"at":    c.at,
		"f.req": string(outerJSON),
	}

	resp, err := c.httpClient.R().
		SetCookies(c.toHttpCookies()...).
		SetFormData(formData).
		SetQueryParam("at", c.at).
		Post(EndpointGenerate)

	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("generate content failed: %d", resp.StatusCode)
	}

	return c.parseResponse(resp.String())
}



func (c *Client) parseResponse(text string) (string, error) {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Gemini response often starts with this magic prefix
		line = strings.TrimPrefix(line, ")]}'")

		var root []interface{}
		if err := json.Unmarshal([]byte(line), &root); err == nil {
			// Iterate through the array of responses in this line
			for _, item := range root {
				// Each item is typically an array itself: ["wrb.fr", "[[...]]", ...]
				itemArray, ok := item.([]interface{})
				if !ok || len(itemArray) < 3 {
					continue
				}

				// The payload is often a JSON string at index 2 (or variable)
				// We specifically look for the candidate structure [rcid, [text, ...], ...] inside the string payload
				// But sometimes the top level structure is simpler.
				
				// Let's look for known markers.
				// Based on Python client: body usually at index 2 of the top level array
				payloadStr, ok := itemArray[2].(string)
				if !ok {
					continue
				}

				var payload []interface{}
				if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
					continue
				}

				// Inside payload, candidates are at index 4 (usually)
				if len(payload) > 4 {
					candidates, ok := payload[4].([]interface{})
					if ok && candidates != nil && len(candidates) > 0 {
						// Found candidates
						firstCandidate, ok := candidates[0].([]interface{})
						if ok && len(firstCandidate) >= 2 {
							// text content part
							contentParts, ok := firstCandidate[1].([]interface{})
							if ok && len(contentParts) > 0 {
								resText, ok := contentParts[0].(string)
								if ok {
									return resText, nil
								}
							}
						}
					}
				}
			}
		}
	}
	
	// Fallback: Dump the first few chars to error for debugging
	debugText := text
	if len(debugText) > 200 {
		debugText = debugText[:200]
	}
	return "", fmt.Errorf("failed to parse valid response from Gemini. Response excerpt: %s", debugText)
}

