package handles

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func testPNG(t *testing.T, width, height int, pixel func(x, y int) color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, pixel(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return buf.Bytes()
}

func TestScoreVideoThumbPrefersContentOverBlack(t *testing.T) {
	black := testPNG(t, 64, 36, func(_, _ int) color.Color {
		return color.Black
	})
	content := testPNG(t, 64, 36, func(x, y int) color.Color {
		if (x/8+y/6)%2 == 0 {
			return color.RGBA{R: 220, G: 60, B: 40, A: 255}
		}
		return color.RGBA{R: 35, G: 170, B: 220, A: 255}
	})

	if got, want := scoreVideoThumb(black), scoreVideoThumb(content); got >= want {
		t.Fatalf("black frame score %.4f should be lower than content score %.4f", got, want)
	}
}

func TestBuildVideoContactSheetKeepsGridAndMissingSlots(t *testing.T) {
	frame := testPNG(t, 32, 18, func(_, _ int) color.Color {
		return color.RGBA{R: 220, G: 60, B: 40, A: 255}
	})
	data, err := buildVideoContactSheet([][]byte{frame})
	if err != nil {
		t.Fatalf("build contact sheet: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode contact sheet: %v", err)
	}
	if got, want := img.Bounds().Dx(), videoContactSheetWidth; got != want {
		t.Fatalf("contact sheet width = %d, want %d", got, want)
	}
	if got, want := img.Bounds().Dy(), videoContactSheetHeight; got != want {
		t.Fatalf("contact sheet height = %d, want %d", got, want)
	}

	r, g, b, _ := color.NRGBAModel.Convert(img.At(videoContactSheetWidth-1, videoContactSheetHeight-1)).RGBA()
	if r>>8 != 16 || g>>8 != 16 || b>>8 != 16 {
		t.Fatalf("missing grid slot has color %d,%d,%d, want dark background", r>>8, g>>8, b>>8)
	}
}

func TestBuildVideoContactSheetAcceptsNineSlots(t *testing.T) {
	frame := testPNG(t, 18, 18, func(_, _ int) color.Color {
		return color.RGBA{R: 60, G: 180, B: 80, A: 255}
	})
	frames := make([][]byte, videoContactSheetColumns*videoContactSheetRows)
	for i := range frames {
		frames[i] = frame
	}
	if _, err := buildVideoContactSheet(frames); err != nil {
		t.Fatalf("build full contact sheet: %v", err)
	}
}

func TestThumbRemoteRiskClassification(t *testing.T) {
	if !isThumbRemoteRiskError(errThumbRemoteRisk) {
		t.Fatal("sentinel risk error should be classified")
	}
	if !isPermanentThumbError(errThumbRemoteRisk) {
		t.Fatal("sentinel risk error should be permanent")
	}
	if !isThumbRemoteRiskError(errors.New("HTTP 403 Forbidden")) {
		t.Fatal("HTTP 403 should be classified as risk")
	}
	if isThumbRemoteRiskError(errors.New("exit status 1")) {
		t.Fatal("generic ffmpeg failure should not be classified as risk")
	}
}

func TestDownloadRangeRejectsIgnoredRangeAtOffset(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "range.bin")
	n, err := downloadRange(context.Background(), &model.Link{URL: server.URL}, dst, 128, 32, "")
	if err == nil {
		t.Fatal("offset Range request should reject an origin that responds with 200")
	}
	if n != 0 {
		t.Fatalf("downloaded %d bytes after rejected response, want 0", n)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not be created after rejected response, stat error: %v", statErr)
	}
}

func TestDownloadRangeBoundsIgnoredInitialRange(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "range.bin")
	n, err := downloadRange(context.Background(), &model.Link{URL: server.URL}, dst, 0, 17, "")
	if err != nil {
		t.Fatalf("download bounded initial response: %v", err)
	}
	if n != 17 {
		t.Fatalf("downloaded %d bytes, want 17", n)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(data, body[:17]) {
		t.Fatalf("destination data %q does not match bounded response", data)
	}
}

func TestDownloadRangeBytesValidatesContentRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Range"), "bytes=10-19"; got != want {
			t.Errorf("Range header = %q, want %q", got, want)
		}
		w.Header().Set("Content-Range", "bytes 10-19/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("abcdefghij"))
	}))
	defer server.Close()

	data, err := downloadRangeBytes(context.Background(), &model.Link{URL: server.URL}, 10, 10, "")
	if err != nil {
		t.Fatalf("download valid Range response: %v", err)
	}
	if got, want := string(data), "abcdefghij"; got != want {
		t.Fatalf("data = %q, want %q", got, want)
	}
}

func TestDownloadRangeRejectsShortPartialContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 10-14/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("abcde"))
	}))
	defer server.Close()

	_, err := downloadRangeBytes(context.Background(), &model.Link{URL: server.URL}, 10, 10, "")
	if err == nil {
		t.Fatal("short Content-Range should be rejected")
	}
}

func TestDownloadRangeRejectsShortBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 10-19/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("abcde"))
	}))
	defer server.Close()

	_, err := downloadRangeBytes(context.Background(), &model.Link{URL: server.URL}, 10, 10, "")
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short body error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestThumbAcquireHonorsContextCancellation(t *testing.T) {
	thumbSemMu.Lock()
	previous := thumbSemCount
	thumbSemCount = 64
	thumbSemMu.Unlock()
	defer func() {
		thumbSemMu.Lock()
		thumbSemCount = previous
		thumbSemMu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if thumbAcquire(ctx, 0) {
		t.Fatal("canceled context should not acquire a saturated thumbnail slot")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled acquire took %s, want prompt return", elapsed)
	}
}

func TestGenerateThumbOnceSuppressesConcurrentWork(t *testing.T) {
	const callers = 24
	var entered atomic.Int32
	var generated atomic.Int32
	start := make(chan struct{})
	release := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)

	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			entered.Add(1)
			data, err := generateThumbOnce("test", t.Name(), func() ([]byte, error) {
				generated.Add(1)
				<-release
				return []byte("ok"), nil
			})
			if err != nil || string(data) != "ok" {
				t.Errorf("generate result = %q, %v", data, err)
			}
		}()
	}
	ready.Wait()
	close(start)
	deadline := time.Now().Add(time.Second)
	for entered.Load() != callers && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if entered.Load() != callers {
		t.Fatalf("only %d/%d callers entered", entered.Load(), callers)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	done.Wait()
	if got := generated.Load(); got != 1 {
		t.Fatalf("generator ran %d times, want 1", got)
	}
}

func TestThumbCacheArtifactFilterProtectsMetadata(t *testing.T) {
	for _, name := range []string{"index.jsonl", "cloud.jsonl", "excluded.jsonl", "video-a.fail", "video-a.tmp.mp4"} {
		if isThumbCacheArtifact(name) {
			t.Fatalf("metadata/temp file %q classified as cache artifact", name)
		}
	}
	for _, name := range []string{"video-a.png", "audio-b.png", "image-c.png", "cover-d.png"} {
		if !isThumbCacheArtifact(name) {
			t.Fatalf("thumbnail file %q not classified as cache artifact", name)
		}
	}
}

func TestThumbObjectFingerprintTracksContentVersionNotDisplayPath(t *testing.T) {
	modified := time.Unix(1_700_000_000, 0)
	base := &model.Object{ID: "object-123", Path: "/driver/a.mp4", Name: "a.mp4", Size: 1024, Modified: modified}
	fingerprint := thumbObjectFingerprint("/__fingerprint_test__/a.mp4", base)
	if !validThumbFingerprint(fingerprint) {
		t.Fatalf("invalid fingerprint %q", fingerprint)
	}

	moved := *base
	moved.Name = "renamed.mp4"
	if got := thumbObjectFingerprint("/__fingerprint_test__/renamed.mp4", &moved); got != fingerprint {
		t.Fatalf("same object ID/version should survive a display-path rename: %s != %s", got, fingerprint)
	}

	replaced := *base
	replaced.ID = "object-456"
	if got := thumbObjectFingerprint("/__fingerprint_test__/a.mp4", &replaced); got == fingerprint {
		t.Fatal("same-path replacement with a new object ID must change fingerprint")
	}

	modifiedAgain := *base
	modifiedAgain.Modified = modified.Add(time.Second)
	if got := thumbObjectFingerprint("/__fingerprint_test__/a.mp4", &modifiedAgain); got == fingerprint {
		t.Fatal("same object ID with a new modification time must change fingerprint")
	}
}
