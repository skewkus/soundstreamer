package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLiveEncodingOnlyRequestedChunksAndSharedRooms(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	path := filepath.Join(root, "long.avi")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=20:duration=20", "-c:v", "mpeg4", "-y", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	m, p, err := probeMedia(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := prepareVideo(ctx, root, m, p, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if !source.Transcode || source.HasAudio {
		t.Fatal("silent AVI metadata incorrect")
	}
	chunks := newVideoChunks(ctx)
	defer chunks.close()
	var results [3]*encodedVideo
	var failures [3]error
	var wait sync.WaitGroup
	for i := range results {
		wait.Add(1)
		go func(i int) { defer wait.Done(); results[i], failures[i] = chunks.get(ctx, source, 0) }(i)
	}
	wait.Wait()
	for i := range results {
		if failures[i] != nil {
			t.Fatal(failures[i])
		}
		if results[i] != results[0] {
			t.Fatal("rooms launched separate encoders for the same chunk")
		}
	}
	chunks.mu.Lock()
	count := len(chunks.jobs)
	chunks.mu.Unlock()
	if count != 1 {
		t.Fatal("encoded unrequested movie chunks")
	}
	if err := os.WriteFile(filepath.Join(root, "first.mp4"), results[0].Data, 0600); err != nil {
		t.Fatal(err)
	}
	fragment, p, err := probeMedia(ctx, filepath.Join(root, "first.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if fragment.Duration < 3.9 || fragment.Duration > 4.1 || p.Streams[0].Codec != "h264" {
		t.Fatal("initial chunk contains the whole movie or wrong codec")
	}
	last, err := chunks.get(ctx, source, 4)
	if err != nil || len(last.Data) == 0 {
		t.Fatalf("seek destination did not encode directly: %v", err)
	}
	chunks.mu.Lock()
	count = len(chunks.jobs)
	chunks.mu.Unlock()
	if count != 2 {
		t.Fatal("seek encoded intervening chunks")
	}
	if _, err := chunks.get(ctx, source, 5); err == nil {
		t.Fatal("accepted a chunk past the end")
	}
	if _, err := chunks.get(ctx, source, -1); err == nil {
		t.Fatal("accepted negative chunk")
	}
	a := testApplication(t)
	a.video = source
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/video?segment=0", nil)
	a.station.handler().ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("X-Video-Codec") == "" || !bytes.Contains(response.Body.Bytes(), []byte("moof")) {
		t.Fatal("live HTTP chunk response invalid")
	}
	response = httptest.NewRecorder()
	a.station.handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/video?segment=99", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatal("invalid chunk request not rejected")
	}
	state := a.clock()
	descriptor, _ := json.Marshal(state["video"])
	if !bytes.Contains(descriptor, []byte(`"transcode":true`)) {
		t.Fatal("client did not receive live playback mode")
	}
}

func TestVideoMemoryCacheIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := newVideoChunks(ctx)
	defer cache.close()
	for i := 0; i < maxVideoCacheEntries+5; i++ {
		job := &videoJob{done: make(chan struct{}), result: &encodedVideo{Data: make([]byte, 100)}, used: time.Now().Add(time.Duration(i) * time.Second)}
		close(job.done)
		cache.jobs[string(rune('a'+i))] = job
	}
	cache.mu.Lock()
	cache.trimLocked()
	count := len(cache.jobs)
	_, oldest := cache.jobs["a"]
	cache.mu.Unlock()
	if count > maxVideoCacheEntries || oldest {
		t.Fatal("old video chunks were not evicted")
	}
	buffer := &boundedBuffer{limit: 4}
	if _, err := buffer.Write([]byte("12345")); err == nil {
		t.Fatal("chunk size limit was ignored")
	}
}
