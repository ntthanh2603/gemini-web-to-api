package providers

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Video URLs are session-bound; API clients should use the local content endpoint.
type Video struct {
	URL       string `json:"url"`
	Thumbnail string `json:"thumbnail,omitempty"`
}
type VideoError struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	ProviderMessage string `json:"provider_message,omitempty"`
}

func (e *VideoError) Error() string         { return e.Message }
func videoError(code, message string) error { return &VideoError{Code: code, Message: message} }

func videoField(value any, index int) any {
	a, ok := value.([]any)
	if !ok || index < 0 {
		return nil
	}
	if index < len(a) && a[index] != nil {
		if _, sparse := a[index].(map[string]any); !sparse {
			return a[index]
		}
	}
	if len(a) > 0 {
		if m, ok := a[len(a)-1].(map[string]any); ok {
			return m[strconv.Itoa(index+1)]
		}
	}
	return nil
}
func videoPath(value any, path ...int) any {
	for _, i := range path {
		value = videoField(value, i)
	}
	return value
}
func videoString(value any) string { s, _ := value.(string); return s }
func candidateVideos(candidate any) []Video {
	// Rich-content field 59 contains the generated-video card. Never infer a
	// generated video from arbitrary links in text, citations or uploaded files.
	card := videoPath(candidate, 12, 59, 0, 0, 0)
	urls, _ := videoPath(card, 0, 7).([]any)
	if len(urls) < 2 {
		return nil
	}
	raw := videoString(urls[1])
	if !trustedVideoURL(raw) {
		return nil
	}
	thumb := videoString(urls[0])
	if !trustedVideoURL(thumb) {
		thumb = ""
	}
	return []Video{{URL: raw, Thumbnail: thumb}}
}
func trustedVideoURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") && isTrustedGoogleMediaHost(u.Hostname())
}

// Decode framed batchexecute responses, retaining complete frames even if the
// stream closes early. Only explicit RPC records are accepted.
func videoRecords(body []byte) [][]any {
	body = bytes.TrimPrefix(bytes.TrimSpace(body), []byte(")]}'"))
	d := json.NewDecoder(bytes.NewReader(body))
	var out [][]any
	var collect func(any)
	collect = func(v any) {
		a, ok := v.([]any)
		if !ok {
			return
		}
		if len(a) > 2 && videoString(a[0]) == "wrb.fr" {
			out = append(out, a)
			return
		}
		for _, x := range a {
			collect(x)
		}
	}
	for {
		var v any
		if d.Decode(&v) != nil {
			break
		}
		collect(v)
	}
	return out
}
func parseVideoGeneration(body []byte) (*Response, error) {
	r := &Response{}
	for _, record := range videoRecords(body) {
		var payload any
		if json.Unmarshal([]byte(videoString(record[2])), &payload) != nil {
			continue
		}
		if cid := videoString(videoPath(payload, 1, 0)); cid != "" {
			r.ConversationID = cid
			r.ResponseID = videoString(videoPath(payload, 1, 1))
		}
		candidates, _ := videoPath(payload, 4).([]any)
		if len(candidates) > 0 {
			candidate := candidates[0]
			r.Videos = append(r.Videos, candidateVideos(candidate)...)
			if s := videoString(videoPath(candidate, 1, 0)); s != "" {
				r.Text = s
			}
		}
	}
	if len(r.Videos) == 0 && r.ConversationID == "" && strings.TrimSpace(r.Text) == "" {
		return nil, videoError("invalid_response", "Gemini returned no video or conversation ID; check Gemini Web before retrying")
	}
	return r, nil
}

type VideoProgress struct {
	ConversationID string
	Message        string
}
type VideoOption func(*videoOptions)
type videoOptions struct{ onStarted func(VideoProgress) }

func WithVideoProgress(callback func(VideoProgress)) VideoOption {
	return func(o *videoOptions) { o.onStarted = callback }
}

var videoLinkPattern = regexp.MustCompile(`(?i)(?:https?:)?//[^\s<>]+`)

func safeVideoMessage(text string) string {
	text = videoLinkPattern.ReplaceAllString(text, "[media link]")
	chars := []rune(strings.TrimSpace(text))
	if len(chars) > 1000 {
		chars = chars[:1000]
	}
	return string(chars)
}
func videoRefusal(text string) *VideoError {
	lower := strings.ToLower(text)
	code := ""
	message := ""
	if (strings.Contains(lower, "limit") || strings.Contains(lower, "quota")) && (strings.Contains(lower, "you have reached") || strings.Contains(lower, "you've reached") || strings.Contains(lower, "you have exceeded") || strings.Contains(lower, "you've used up")) {
		code = "rate_limited"
		message = "Gemini reports a usage limit; wait for the reset shown in Gemini Web"
	}
	if code == "" && (strings.Contains(lower, "can't generate") || strings.Contains(lower, "cannot generate") || strings.Contains(lower, "unable to generate")) {
		code = "no_video"
		message = "Gemini declined this video request; see its explanation"
	}
	if code == "" {
		return nil
	}
	return &VideoError{Code: code, Message: message, ProviderMessage: safeVideoMessage(text)}
}

// GenerateVideo submits once and then reads the same conversation until completion.
func (c *Client) GenerateVideo(ctx context.Context, prompt, model string, options ...VideoOption) (*Video, error) {
	config := videoOptions{}
	for _, option := range options {
		option(&config)
	}
	r, err := c.GenerateContent(ctx, "Generate a video (not a storyboard or an image): "+prompt, WithModel(model), func(cfg *GenerateConfig) { cfg.videoGeneration = true })
	if err != nil {
		var ve *VideoError
		if errors.As(err, &ve) {
			return nil, ve
		}
		return nil, videoError("upstream_error", "Gemini could not start video generation; verify cookies, account access and quota in Gemini Web before retrying")
	}
	if config.onStarted != nil {
		config.onStarted(VideoProgress{ConversationID: r.ConversationID, Message: safeVideoMessage(r.Text)})
	}
	if len(r.Videos) > 0 {
		return &r.Videos[0], nil
	}
	if refusal := videoRefusal(r.Text); refusal != nil {
		return nil, refusal
	}
	if r.ConversationID == "" {
		return nil, &VideoError{Code: "no_video", Message: "Gemini returned no video or conversation to poll", ProviderMessage: safeVideoMessage(r.Text)}
	}
	return c.waitVideo(ctx, r, 10*time.Second)
}

func (c *Client) waitVideo(ctx context.Context, r *Response, interval time.Duration) (*Video, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastPollError error

	for {
		select {
		case <-ctx.Done():
			if lastPollError != nil {
				return nil, &VideoError{Code: "poll_failed", Message: "Could not read this conversation before the deadline; check Gemini Web before resubmitting", ProviderMessage: safeVideoMessage(r.Text)}
			}
			return nil, videoError("generation_timeout", "Video did not finish before the deadline; check Gemini Web before starting another job")
		case <-ticker.C:
			video, done, err := c.readVideo(ctx, r.ConversationID)
			if err != nil {
				lastPollError = err
				var ve *VideoError
				if errors.As(err, &ve) && (ve.Code == "access_denied" || ve.Code == "rate_limited" || ve.Code == "no_video") {
					return nil, err
				}
				continue
			}
			lastPollError = nil
			if video != nil {
				return video, nil
			}
			if done {
				return nil, videoError("no_video", "Gemini finished without a video; check the conversation in Gemini Web for account, quota or prompt restrictions")
			}
		}
	}
}
func (c *Client) readVideo(ctx context.Context, cid string) (*Video, bool, error) {
	c.mu.Lock()
	token, cookies, bl, sid, lang, account, generation := c.at, c.cookieHeader, c.buildLabel, c.sessionID, c.language, c.authUser, c.generationID
	c.mu.Unlock()
	payload, _ := json.Marshal([]any{cid, 1, nil, 1, []int{1}, []int{4}, nil, 1})
	envelope, _ := json.Marshal([]any{[]any{[]any{"hNvQHb", string(payload), nil, "generic"}}})
	query := url.Values{"rpcids": {"hNvQHb"}, "rt": {"c"}, "hl": {lang}, "source-path": {geminiSourcePath(account)}}
	if bl != "" {
		query.Set("bl", bl)
	}
	if sid != "" {
		query.Set("f.sid", sid)
	}
	form := url.Values{"at": {token}, "f.req": {string(envelope)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiAccountURL(EndpointBatchExec, account)+"?"+query.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, false, videoError("poll_failed", "Could not prepare video status request")
	}
	for k, v := range DefaultHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("Cookie", cookies)
	batchHeader := make([]any, 17)
	batchHeader[0] = 1
	batchHeader[8] = []int{4, 5, 6, 8}
	batchHeader[16] = generation
	headerJSON, _ := json.Marshal(batchHeader)
	req.Header.Set(geminiModelHeaderKey, string(headerJSON))
	req.Header.Set("x-goog-ext-73010989-jspb", "[0]")
	resp, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return nil, false, videoError("poll_failed", "Could not read video status; check connectivity and Gemini Web before retrying")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, false, videoError("access_denied", "Gemini denied access to the conversation; verify the browser session")
	}
	if resp.StatusCode != 200 {
		return nil, false, videoError("poll_failed", fmt.Sprintf("Video status returned HTTP %d; verify the Gemini session", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return nil, false, videoError("poll_failed", "Could not read video status")
	}
	if len(body) > 8<<20 {
		return nil, false, videoError("poll_failed", "Video status response exceeds the size limit")
	}
	return parseVideoHistory(body)
}
func parseVideoHistory(body []byte) (*Video, bool, error) {
	recognized, done := false, false
	var terminalError error
	for _, record := range videoRecords(body) {
		if videoString(record[1]) != "hNvQHb" {
			continue
		}
		if rejection, ok := videoField(record, 5).([]any); ok {
			code, _ := videoField(rejection, 0).(float64)
			switch code {
			case 7, 16:
				return nil, false, videoError("access_denied", "Gemini rejected access to this conversation; verify the browser session")
			case 8:
				return nil, false, videoError("rate_limited", "Gemini limited status requests; wait before checking again")
			}
		}
		var payload []any
		if json.Unmarshal([]byte(videoString(record[2])), &payload) != nil || payload == nil {
			continue
		}
		turns, ok := videoField(payload, 0).([]any)
		if !ok {
			continue
		}
		recognized = true
		if len(turns) == 0 {
			continue
		}
		candidates, _ := videoPath(turns[0], 3, 0).([]any)
		if len(candidates) == 0 {
			continue
		}
		candidate := candidates[0]
		if videos := candidateVideos(candidate); len(videos) > 0 {
			return &videos[0], true, nil
		}
		if refusal := videoRefusal(videoString(videoPath(candidate, 1, 0))); refusal != nil {
			terminalError = refusal
			continue
		}
		if videoPath(candidate, 12, 59) != nil || videoPath(candidate, 12, 64) != nil {
			continue
		}
		status, _ := videoPath(candidate, 8, 0).(float64)
		if status == 2 || (status == 1 && videoPath(candidate, 12, 6, 0) == nil) {
			done = true
			terminalError = &VideoError{Code: "no_video", Message: "Gemini finished without a video", ProviderMessage: safeVideoMessage(videoString(videoPath(candidate, 1, 0)))}
		}
	}
	if terminalError != nil {
		return nil, true, terminalError
	}
	if recognized {
		return nil, done, nil
	}
	return nil, false, videoError("poll_failed", "Gemini returned an unrecognized video status response")
}

// DownloadVideo writes a bounded MP4 to a caller-owned file. Cookies and signed
// URLs are never returned in errors. Redirects are checked before credentials
// are attached to each request.
func (c *Client) DownloadVideo(ctx context.Context, video Video, dst io.Writer) error {
	if !trustedVideoURL(video.URL) {
		return videoError("download_failed", "Untrusted video download location")
	}
	c.mu.Lock()
	cookies := c.cookieHeader
	c.mu.Unlock()
	client := generatedImageHTTPClient(cookies)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !trustedVideoURL(req.URL.String()) {
			return errors.New("untrusted video redirect")
		}
		req.Header.Set("Cookie", cookies)
		req.Header.Set("Referer", "https://gemini.google.com/")
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, video.URL, nil)
	if err != nil {
		return videoError("download_failed", "Invalid video download location")
	}
	req.Header.Set("Cookie", cookies)
	req.Header.Set("Referer", "https://gemini.google.com/")
	resp, err := client.Do(req)
	if err != nil {
		return videoError("download_failed", "Video download failed; the session or link may have expired")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return videoError("download_failed", fmt.Sprintf("Video download returned HTTP %d", resp.StatusCode))
	}
	const limit = 100 << 20
	if resp.ContentLength > limit {
		return videoError("video_too_large", "Video exceeds the 100 MiB download limit")
	}
	n, err := copyMP4(dst, resp.Body, limit)
	if err != nil {
		return err
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		return videoError("download_failed", "Video download was truncated")
	}
	return nil
}

// Validate bounded top-level ISO BMFF boxes while streaming to disk. A MIME
// type or ftyp prefix alone also accepts truncated files and non-video assets.
func copyMP4(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	header := make([]byte, 12)
	if _, err := io.ReadFull(src, header); err != nil || string(header[4:8]) != "ftyp" || !mp4Brand(string(header[8:12])) {
		return 0, videoError("download_failed", "Gemini did not return an MP4 video")
	}
	reader := &io.LimitedReader{R: io.MultiReader(bytes.NewReader(header), src), N: limit + 1}
	var total int64
	var movie, media bool
	for boxes := 0; boxes < 10000; boxes++ {
		h := make([]byte, 8)
		n, err := io.ReadFull(reader, h)
		if err == io.EOF && n == 0 {
			if movie && media {
				return total, nil
			}
			break
		}
		if err != nil {
			return total, videoError("download_failed", "Video download was truncated")
		}
		size := uint64(binary.BigEndian.Uint32(h[:4]))
		kind := string(h[4:])
		headerSize := uint64(8)
		if size == 1 {
			extra := make([]byte, 8)
			if _, err := io.ReadFull(reader, extra); err != nil {
				return total, videoError("download_failed", "Video download was truncated")
			}
			h = append(h, extra...)
			size = binary.BigEndian.Uint64(extra)
			headerSize = 16
		}
		if size != 0 && size < headerSize {
			return total, videoError("download_failed", "Invalid MP4 box size")
		}
		if size > uint64(limit-total) {
			return total, videoError("video_too_large", "Video exceeds the download limit")
		}
		if n, err := dst.Write(h); err != nil || n != len(h) {
			return total, videoError("storage_error", "Could not save video")
		}
		total += int64(headerSize)
		var copied int64
		if size == 0 {
			copied, err = io.Copy(dst, reader)
		} else {
			copied, err = io.CopyN(dst, reader, int64(size-headerSize))
		}
		total += copied
		if total > limit {
			return total, videoError("video_too_large", "Video exceeds the download limit")
		}
		if err != nil {
			return total, videoError("download_failed", "Video transfer or storage was interrupted")
		}
		if kind == "moov" && copied > 0 {
			movie = true
		}
		if kind == "mdat" && copied > 0 {
			media = true
		}
		if size == 0 {
			if movie && media {
				return total, nil
			}
			break
		}
	}
	return total, videoError("download_failed", "MP4 is missing video metadata or media data")
}

func mp4Brand(brand string) bool {
	switch brand {
	case "isom", "iso2", "iso3", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "dash":
		return true
	}
	return false
}
