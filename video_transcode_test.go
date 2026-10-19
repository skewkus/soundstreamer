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
	"testing"
	"time"
)

func TestVideoTranscodeSettingsPersistenceAndAdminAccess(t *testing.T) {
	a := testApplication(t)
	if a.config.ForceVideoTranscode || a.config.VideoTranscodeResolution != "720p" {
		t.Fatal("incorrect defaults")
	}
	for _, resolution := range []string{"original", "1080p", "720p", "480p"} {
		if err := a.action(actionRequest{Action: "videoTranscodeSettings", Enabled: true, Name: resolution}); err != nil {
			t.Fatal(err)
		}
		saved, err := loadSettings(a.root)
		if err != nil || !saved.ForceVideoTranscode || saved.VideoTranscodeResolution != resolution {
			t.Fatal("settings were not persisted")
		}
	}
	if a.action(actionRequest{Action: "videoTranscodeSettings", Name: "4k"}) == nil || a.config.VideoTranscodeResolution != "480p" {
		t.Fatal("invalid profile accepted or partially saved")
	}
	r := httptest.NewRequest("POST", "/api/control/action", bytes.NewBufferString(`{"action":"videoTranscodeSettings","enabled":false,"name":"original"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.station.handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("public API changed video settings")
	}
	if a.dispatchAction(actionRequest{Action: "videoTranscodeSettings", Name: "720p"}, true) == nil {
		t.Fatal("public dispatcher changed video settings")
	}
	// Saving quality while paused must not start playback or reset its position.
	a.config.Current = &media{ID: "paused", Kind: "video", Path: "not-started.mp4", Duration: 90}
	a.position = 17
	if err := a.action(actionRequest{Action: "videoTranscodeSettings", Enabled: false, Name: "720p"}); err != nil {
		t.Fatal(err)
	}
	if a.playing || a.loading || a.position != 17 {
		t.Fatal("quality setting started paused video")
	}
}

func TestForcedVideoProfilesEncodeCompatibleSourcesAndKeepCacheSeparate(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	path := filepath.Join(root, "native.mp4")
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=12:duration=1.2", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono", "-t", "1.2", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-y", path}
	if out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	m, p, err := probeMedia(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := prepareVideo(ctx, root, m, p, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if original.Transcode || !original.HasAudio {
		t.Fatal("fixture should play directly")
	}
	if forcedVideoSource(original, false, "480p") != original {
		t.Fatal("disabled setting changed automatic compatibility decision")
	}
	// This already-unsupported descriptor must keep automatic transcoding even off.
	unsupported := *original
	unsupported.Transcode = true
	if !forcedVideoSource(&unsupported, false, "720p").Transcode {
		t.Fatal("disabled force switch disabled automatic transcoding")
	}
	// Apply a profile during playback, retaining the position and shared PCM path.
	a := testApplication(t)
	a.play(m, 0.35)
	waitPlaybackState(t, a, func() bool { return a.playing && a.video != nil && !a.video.Transcode })
	a.mu.Lock()
	beforePosition, beforeSession := a.positionLocked(time.Now()), a.session
	a.mu.Unlock()
	if err := a.action(actionRequest{Action: "videoTranscodeSettings", Enabled: true, Name: "720p"}); err != nil {
		t.Fatal(err)
	}
	waitPlaybackState(t, a, func() bool { return a.playing && a.video != nil && a.video.TranscodeProfile == "720p" })
	a.mu.Lock()
	keptPosition := a.position >= beforePosition && a.position < beforePosition+0.3
	newSession := a.session != beforeSession
	a.mu.Unlock()
	if !keptPosition || !newSession {
		t.Fatal("applying preset lost position or failed to reset room playback")
	}
	if err := a.action(actionRequest{Action: "videoTranscodeSettings", Enabled: false, Name: "720p"}); err != nil {
		t.Fatal(err)
	}
	waitPlaybackState(t, a, func() bool { return a.playing && a.video != nil && !a.video.Transcode })
	if err := a.action(actionRequest{Action: "stop"}); err != nil {
		t.Fatal(err)
	}
	cache := newVideoChunks(ctx)
	defer cache.close()
	for _, resolution := range []string{"original", "1080p", "720p", "480p"} {
		source := forcedVideoSource(original, true, resolution)
		if !source.Transcode || !source.HasAudio || source.Duration != original.Duration || source.Key == original.Key {
			t.Fatal("forced source corrupted audio, duration or cache identity")
		}
		encoded, err := cache.get(ctx, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		again, err := cache.get(ctx, source, 0)
		if err != nil || again != encoded {
			t.Fatal("rooms did not reuse the same encoded chunk")
		}
		fragment := filepath.Join(root, resolution+".mp4")
		if err := os.WriteFile(fragment, encoded.Data, 0600); err != nil {
			t.Fatal(err)
		}
		width, height := probeEncodedDimensions(t, ctx, fragment)
		preset := videoTranscodePresetFor(resolution)
		if preset.Height == 0 {
			preset.Width, preset.Height = 1920, 1080
		}
		if width > preset.Width || height > preset.Height || width%2 != 0 || height%2 != 0 || width < 2 || height < 2 {
			t.Fatalf("%s produced invalid dimensions %dx%d", resolution, width, height)
		}
		if resolution == "720p" && (width != 1280 || height != 720) {
			t.Fatalf("720p produced %dx%d", width, height)
		}
		if original.Transcode || original.TranscodeProfile != "" {
			t.Fatal("forced conversion mutated a shared source")
		}
	}
	cache.mu.Lock()
	count := len(cache.jobs)
	cache.mu.Unlock()
	if count != 4 {
		t.Fatal("different qualities reused the same cached chunk")
	}
	// An already-small source must retain its size even with the 1080p preset.
	small := filepath.Join(root, "small.avi")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=12:duration=1.2", "-c:v", "mpeg4", "-y", small).CombinedOutput(); err != nil {
		t.Fatalf("small fixture: %v %s", err, out)
	}
	sm, sp, err := probeMedia(ctx, small)
	if err != nil {
		t.Fatal(err)
	}
	sv, err := prepareVideo(ctx, root, sm, sp, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	result, err := cache.get(ctx, forcedVideoSource(sv, true, "1080p"), 0)
	if err != nil {
		t.Fatal(err)
	}
	smallOutput := filepath.Join(root, "small-output.mp4")
	if err := os.WriteFile(smallOutput, result.Data, 0600); err != nil {
		t.Fatal(err)
	}
	width, height := probeEncodedDimensions(t, ctx, smallOutput)
	if width != 160 || height != 90 {
		t.Fatalf("small source was enlarged to %dx%d", width, height)
	}
}

func probeEncodedDimensions(t *testing.T, ctx context.Context, path string) (int, int) {
	t.Helper()
	output, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=width,height,codec_name,pix_fmt", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Streams []struct {
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Codec  string `json:"codec_name"`
			Format string `json:"pix_fmt"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &probe); err != nil || len(probe.Streams) != 1 {
		t.Fatal("invalid encoded streams")
	}
	stream := probe.Streams[0]
	if stream.Codec != "h264" || stream.Format != "yuv420p" {
		t.Fatal("forced output not compatible H.264")
	}
	return stream.Width, stream.Height
}
