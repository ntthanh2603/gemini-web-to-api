package openai

import (
	"context"
	"encoding/json"
	"errors"
	"gemini-web-to-api/internal/modules/providers"
	"github.com/gofiber/fiber/v3"
	"go.uber.org/fx"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type edgeVideoBackend struct {
	fakeVideo
	unhealthy     bool
	panicGenerate bool
	downloadErr   bool
	calls         atomic.Int32
}

func (f *edgeVideoBackend) IsHealthy() bool { return !f.unhealthy }
func (f *edgeVideoBackend) GenerateVideo(ctx context.Context, p, m string, options ...providers.VideoOption) (*providers.Video, error) {
	f.calls.Add(1)
	if f.panicGenerate {
		panic("sensitive provider detail")
	}
	return f.fakeVideo.GenerateVideo(ctx, p, m, options...)
}
func (f *edgeVideoBackend) DownloadVideo(ctx context.Context, v providers.Video, w io.Writer) error {
	io.WriteString(w, "partial data")
	if f.downloadErr {
		return errors.New("secret storage error")
	}
	return nil
}
func edgeRequest(t *testing.T, app *fiber.App, method, path, body string) (int, []byte) {
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
func TestVideoConcurrentAdmissionAndShutdown(t *testing.T) {
	backend := &edgeVideoBackend{fakeVideo: fakeVideo{wait: make(chan struct{})}}
	v := newVideoController(backend)
	app := fiber.New()
	v.register(app.Group("/openai/v1"))
	defer v.close(context.Background())
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _ := edgeRequest(t, app, "POST", "/openai/v1/videos", `{"prompt":"test"}`)
			if status == 202 {
				accepted.Add(1)
			} else if status != 429 {
				t.Errorf("status %d", status)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("multiple active submissions", accepted.Load())
	}
	if err := v.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(v.dir); !os.IsNotExist(err) {
		t.Fatal("shutdown leaked files")
	}
	if status, _ := edgeRequest(t, app, "POST", "/openai/v1/videos", `{"prompt":"test"}`); status != 503 {
		t.Fatal("accepted during shutdown")
	}
}
func TestVideoWorkerFailuresCleanFiles(t *testing.T) {
	for _, tc := range []struct {
		name       string
		backend    *edgeVideoBackend
		missingDir bool
		code       string
	}{
		{"panic", &edgeVideoBackend{panicGenerate: true}, false, "internal_error"},
		{"download", &edgeVideoBackend{downloadErr: true}, false, "video_failed"},
		{"storage", &edgeVideoBackend{}, true, "storage_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newVideoController(tc.backend)
			defer v.close(context.Background())
			v.dir = t.TempDir()
			if tc.missingDir {
				v.dir = filepath.Join(v.dir, "not-a-directory")
				if err := os.WriteFile(v.dir, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			job := videoJob{ID: "test", Status: "in_progress"}
			v.jobs[job.ID] = job
			v.wg.Add(1)
			v.run(job, "test")
			result := v.jobs[job.ID]
			if result.Status != "failed" || result.Error.Code != tc.code {
				t.Fatalf("%+v", result)
			}
			b, _ := json.Marshal(result)
			if strings.Contains(string(b), "sensitive") || strings.Contains(string(b), "secret") {
				t.Fatal("leaked error")
			}
			if tc.missingDir && tc.backend.calls.Load() != 0 {
				t.Fatal("spent quota despite storage failure")
			}
			if entries, _ := os.ReadDir(v.dir); len(entries) != 0 {
				t.Fatal("partial file leaked")
			}
		})
	}
}
func TestVideoInputAndCapacityBoundaries(t *testing.T) {
	backend := &edgeVideoBackend{fakeVideo: fakeVideo{wait: make(chan struct{})}}
	v := newVideoController(backend)
	defer v.close(context.Background())
	app := fiber.New()
	v.register(app.Group("/openai/v1"))
	for _, body := range []string{`null`, `[]`, `{"prompt":null}`, `{"prompt":123}`, `{"prompt":"  "}`, `{"prompt":"ok","n":1}`, `{"prompt":"x"}null`, `{"prompt":"` + strings.Repeat("x", 8001) + `"}`} {
		if status, _ := edgeRequest(t, app, "POST", "/openai/v1/videos", body); status != 400 {
			t.Fatalf("invalid accepted %d", status)
		}
	}
	backend.unhealthy = true
	if status, _ := edgeRequest(t, app, "POST", "/openai/v1/videos", `{"prompt":"x"}`); status != 503 {
		t.Fatal(status)
	}
	backend.unhealthy = false
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		v.jobs[id] = videoJob{ID: id, Status: "failed", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	}
	if status, _ := edgeRequest(t, app, "POST", "/openai/v1/videos", `{"prompt":"x"}`); status != 429 {
		t.Fatal("capacity", status)
	}
	for id, job := range v.jobs {
		job.ExpiresAt = 0
		v.jobs[id] = job
	}
	if status, _ := edgeRequest(t, app, "POST", "/openai/v1/videos", `{"prompt":"`+strings.Repeat("x", 8000)+`"}`); status != 202 {
		t.Fatal("boundary", status)
	}
}
func TestVideoRouteLifecycle(t *testing.T) {
	app := fiber.New()
	application := fx.New(fx.Supply(app, &providers.Client{}), fx.Invoke(registerVideoRoutes), fx.NopLogger)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if status, _ := edgeRequest(t, app, "GET", "/openai/v1/videos/not-found", ""); status != 404 {
		t.Fatal(status)
	}
	if err := application.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestVideoRecoversWhenIdleCacheDirectoryDisappears(t *testing.T) {
	backend := &edgeVideoBackend{}
	v := newVideoController(backend)
	defer v.close(context.Background())
	v.dir = t.TempDir()
	oldDirectory := v.dir
	// An external temporary-directory cleaner removes the idle cache.
	if err := os.Remove(oldDirectory); err != nil {
		t.Fatal(err)
	}
	job := videoJob{ID: "recovered", Status: "in_progress"}
	v.jobs[job.ID] = job
	v.wg.Add(1)
	v.run(job, "test")
	result := v.jobs[job.ID]
	if result.Status != "completed" || backend.calls.Load() != 1 {
		t.Fatalf("recovery failed: %+v", result)
	}
	if v.dir == oldDirectory {
		t.Fatal("must allocate a fresh private directory")
	}
	if _, err := os.Stat(result.path); err != nil {
		t.Fatal(err)
	}
}
func TestVideoStorageRecoveryDoesNotRunAfterShutdown(t *testing.T) {
	v := newVideoController(&edgeVideoBackend{})
	if err := v.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := v.reserveVideoFile(); !errors.Is(err, context.Canceled) {
		t.Fatalf("storage allocated after shutdown: %v", err)
	}
}
