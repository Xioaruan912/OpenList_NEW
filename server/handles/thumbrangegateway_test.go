package handles

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

type memoryThumbRangeReader struct {
	data   []byte
	mu     sync.Mutex
	ranges []http_range.Range
}

func (r *memoryThumbRangeReader) RangeRead(ctx context.Context, hr http_range.Range) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	length := hr.Length
	if length < 0 || hr.Start+length > int64(len(r.data)) {
		length = int64(len(r.data)) - hr.Start
	}
	if hr.Start < 0 || length < 0 || hr.Start+length > int64(len(r.data)) {
		return nil, io.ErrUnexpectedEOF
	}
	r.mu.Lock()
	r.ranges = append(r.ranges, http_range.Range{Start: hr.Start, Length: length})
	r.mu.Unlock()
	return io.NopCloser(bytes.NewReader(r.data[hr.Start : hr.Start+length])), nil
}

func (r *memoryThumbRangeReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ranges)
}

func loopbackHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil}}
}

func TestThumbRangeGatewayServesRangeAndExpiresToken(t *testing.T) {
	reader := &memoryThumbRangeReader{data: []byte("0123456789abcdefghijklmnopqrstuvwxyz")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gatewayURL, cleanup, err := registerThumbRangeGateway(ctx, "/movie/test.mp4", reader, int64(len(reader.data)))
	if err != nil {
		t.Fatalf("register gateway: %v", err)
	}
	client := loopbackHTTPClient()

	req, err := http.NewRequest(http.MethodGet, gatewayURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=10-19")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET range: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Range"), "bytes 10-19/36"; got != want {
		t.Fatalf("Content-Range = %q, want %q", got, want)
	}
	if got, want := string(body), "abcdefghij"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	head, err := http.NewRequest(http.MethodHead, gatewayURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	headResp, err := client.Do(head)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	_ = headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK || headResp.ContentLength != int64(len(reader.data)) {
		t.Fatalf("HEAD status/length = %d/%d", headResp.StatusCode, headResp.ContentLength)
	}

	cleanup()
	resp, err = client.Get(gatewayURL)
	if err != nil {
		t.Fatalf("GET expired token: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expired token status = %d, want 404", resp.StatusCode)
	}
}

func TestThumbRangeGatewayCapsSingleRead(t *testing.T) {
	r, ok := boundThumbGatewayRange(http_range.Range{
		Start:  0,
		Length: thumbRangeGatewayMaxRead + 1024,
	}, thumbRangeGatewayMaxRead+4096)
	if !ok {
		t.Fatal("expected range to be valid")
	}
	if r.Length != thumbRangeGatewayMaxRead {
		t.Fatalf("bounded length = %d, want %d", r.Length, thumbRangeGatewayMaxRead)
	}
}

func TestThumbRangeGatewayWorksWithFFmpegAndFFprobe(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}

	mediaPath := filepath.Join(t.TempDir(), "gateway.mp4")
	cmd := exec.Command(ffmpegPath,
		"-v", "error",
		"-f", "lavfi", "-i", "color=c=blue:s=64x64:d=1",
		"-c:v", "mpeg4", "-movflags", "+faststart",
		"-y", mediaPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("local ffmpeg cannot create integration fixture: %v: %s", err, out)
	}
	data, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	reader := &memoryThumbRangeReader{data: data}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gatewayURL, cleanup, err := registerThumbRangeGateway(ctx, "/virtual/gateway.mp4", reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	probe := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		gatewayURL,
	)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("ffprobe over RangeReader gateway failed: %v: %s", err, out)
	}
	frame, err := extractVideoFrameAt(ctx, gatewayURL, nil, "", "0.2")
	if err != nil {
		t.Fatalf("ffmpeg frame extraction over RangeReader gateway failed: %v", err)
	}
	if len(frame) == 0 {
		t.Fatal("ffmpeg returned empty frame")
	}
	if reader.callCount() == 0 {
		t.Fatal("ffmpeg/ffprobe never used RangeReader gateway")
	}
}
