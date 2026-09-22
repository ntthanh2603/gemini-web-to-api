package openai

import (
	"context"
	"encoding/json"
	"errors"
	"gemini-web-to-api/internal/modules/providers"
	"github.com/gofiber/fiber/v3"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeVideo struct {
	wait chan struct{}
	err  error
}

func (f *fakeVideo) IsHealthy() bool { return true }
func (f *fakeVideo) ResolveModel(s string) (providers.ModelInfo, error) {
	if s == "bad" {
		return providers.ModelInfo{}, errors.New("bad")
	}
	return providers.ModelInfo{ID: "gemini-pro"}, nil
}
func (f *fakeVideo) GenerateVideo(ctx context.Context, p, m string, options ...providers.VideoOption) (*providers.Video, error) {
	if f.wait != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.wait:
		}
	}
	return &providers.Video{}, f.err
}
func (f *fakeVideo) DownloadVideo(ctx context.Context, v providers.Video, w io.Writer) error {
	_, err := io.WriteString(w, "MP4-test")
	return err
}
func TestVideoJobAPI(t *testing.T) {
	backend := &fakeVideo{wait: make(chan struct{})}
	v := newVideoController(backend)
	defer v.close(context.Background())
	app := fiber.New()
	v.register(app.Group("/openai/v1"))
	request := func(method, path, body string) (int, []byte) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	for _, body := range []string{`{}`, `{"prompt":"x","seconds":8}`, `{"prompt":"x","model":"bad"}`, `{"prompt":"x"} {}`} {
		if s, _ := request("POST", "/openai/v1/videos", body); s != 400 {
			t.Fatal(s)
		}
	}
	status, b := request("POST", "/openai/v1/videos", `{"prompt":"a boy walking"}`)
	if status != 202 {
		t.Fatalf("%d %s", status, b)
	}
	var job videoJob
	json.Unmarshal(b, &job)
	base := "/openai/v1/videos/" + job.ID
	if s, _ := request("POST", "/openai/v1/videos", `{"prompt":"second"}`); s != 429 {
		t.Fatal("duplicate", s)
	}
	if s, _ := request("GET", base+"/content", ""); s != 409 {
		t.Fatal(s)
	}
	close(backend.wait)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, b = request("GET", base, "")
		json.Unmarshal(b, &job)
		if job.Status == "completed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if job.Status != "completed" {
		t.Fatalf("%s", b)
	}
	if s, b := request("GET", base+"/content", ""); s != 200 || string(b) != "MP4-test" {
		t.Fatalf("%d %s", s, b)
	}
	v.mu.Lock()
	stored := v.jobs[job.ID]
	path := stored.path
	stored.ExpiresAt = 0
	v.jobs[job.ID] = stored
	v.mu.Unlock()
	if s, _ := request("GET", base, ""); s != 404 {
		t.Fatal(s)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file not cleaned")
	}
}
func TestVideoFailureDoesNotExposeSecrets(t *testing.T) {
	v := newVideoController(&fakeVideo{err: errors.New("Cookie=private; https://secret-url")})
	defer v.close(context.Background())
	job := videoJob{ID: "test", Status: "in_progress"}
	v.jobs[job.ID] = job
	v.wg.Add(1)
	v.run(job, "prompt")
	b, _ := json.Marshal(v.jobs[job.ID])
	if strings.Contains(string(b), "private") || v.jobs[job.ID].Status != "failed" {
		t.Fatal(string(b))
	}
}
