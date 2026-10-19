package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testApplication(t *testing.T) *application {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a, err := newApplication(ctx, t.TempDir(), 1500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); a.shutdown() })
	return a
}

func TestQueueIdentityPersistenceAndPlaylists(t *testing.T) {
	a := testApplication(t)
	m := media{ID: "track", Path: "example.mp3", Title: "Example", Kind: "audio", Duration: 90}
	a.library[m.ID] = m
	for i := 0; i < 3; i++ {
		if err := a.action(actionRequest{Action: "enqueue", ID: m.ID}); err != nil {
			t.Fatal(err)
		}
	}
	first, second := a.config.Queue[0].Key, a.config.Queue[1].Key
	if first == second {
		t.Fatal("duplicate songs need independent queue identities")
	}
	if err := a.action(actionRequest{Action: "move", Key: first, To: 2}); err != nil {
		t.Fatal(err)
	}
	if a.config.Queue[2].Key != first || a.config.Queue[0].Key != second {
		t.Fatal("move lost queue order")
	}
	if err := a.action(actionRequest{Action: "remove", Key: second}); err != nil {
		t.Fatal(err)
	}
	if len(a.config.Queue) != 2 || a.config.Queue[1].Key != first {
		t.Fatal("remove targeted wrong duplicate")
	}
	if err := a.action(actionRequest{Action: "move", Key: first, To: 99}); err == nil {
		t.Fatal("invalid move accepted")
	}
	if len(a.config.Queue) != 2 {
		t.Fatal("invalid move mutated queue")
	}
	if err := a.action(actionRequest{Action: "savePlaylist", Name: "House"}); err != nil {
		t.Fatal(err)
	}
	if err := a.action(actionRequest{Action: "clearQueue"}); err != nil {
		t.Fatal(err)
	}
	if err := a.action(actionRequest{Action: "loadPlaylist", Name: "House"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSettings(a.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Queue) != 2 || len(loaded.Playlists["House"]) != 2 {
		t.Fatal("queue/playlist did not survive save")
	}
	if loaded.Queue[0].Key == first {
		t.Fatal("loading playlist reused queue identities")
	}
}

func TestPauseCapturesOutputPositionAndSeekRemainsPaused(t *testing.T) {
	a := testApplication(t)
	a.config.Current = &media{ID: "movie", Kind: "video", Duration: 100}
	a.position = 20
	a.playing = true
	a.anchorMs = float64(time.Since(a.station.origin))/float64(time.Millisecond) - 3000
	oldSession := a.session
	if err := a.action(actionRequest{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	if a.position < 22.9 || a.position > 23.2 || a.playing || a.session == oldSession {
		t.Fatalf("pause state: %.3f %v", a.position, a.playing)
	}
	if err := a.action(actionRequest{Action: "seek", Position: 45}); err != nil {
		t.Fatal(err)
	}
	if a.position != 45 || a.playing || a.loading {
		t.Fatal("paused seek unexpectedly started playback")
	}
	loaded, err := loadSettings(a.root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Position != 45 || loaded.Current.ID != "movie" || loaded.ResumeOnStartup {
		t.Fatal("restore settings incorrect")
	}
	a.anchorMs = float64(time.Since(a.station.origin))/float64(time.Millisecond) + 1500
	a.playing = true
	if a.positionLocked(time.Now()) != 45 {
		t.Fatal("playback advanced during initial buffer")
	}
	a.playing = false
}

func TestAdminAccessAndRoomMute(t *testing.T) {
	a := testApplication(t)
	if err := a.changePassword("", "test-password", true); err != nil {
		t.Fatal(err)
	}
	h := a.station.handler()
	call := func(method, path string, payload any, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		var b []byte
		if payload != nil {
			b, _ = json.Marshal(payload)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if call("GET", "/api/admin/state", nil, nil, "").Code != 401 {
		t.Fatal("admin exposed without login")
	}
	if call("GET", "/api/admin/folders", nil, nil, "").Code != 401 {
		t.Fatal("filesystem exposed without login")
	}
	login := call("POST", "/api/admin/login", map[string]string{"password": "test-password"}, nil, "")
	if login.Code != 200 {
		t.Fatal("login failed")
	}
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("admin cookie permissions missing")
	}
	if call("POST", "/api/admin/action", actionRequest{Action: "clearQueue"}, cookie, "http://other.example").Code != 403 {
		t.Fatal("cross-origin control accepted")
	}
	a.library["a"] = media{ID: "a", Title: "Alpha", Artist: "Person", Kind: "audio"}
	a.library["b"] = media{ID: "b", Title: "Beta", Artist: "Zulu", Kind: "video"}
	list := call("GET", "/api/admin/library?limit=1&offset=1", nil, cookie, "")
	if !strings.Contains(list.Body.String(), "Beta") || strings.Contains(list.Body.String(), "Alpha") {
		t.Fatal("pagination incorrect")
	}
	if call("GET", "/api/admin/library?q=person&kind=audio", nil, cookie, "").Code != 200 {
		t.Fatal("search failed")
	}
	id := "lounge-device-123"
	a.roomConnection(id, 1)
	if call("POST", "/api/room", map[string]string{"id": id, "name": "Lounge TV", "kind": "watch"}, nil, "").Code != 200 {
		t.Fatal("room naming failed")
	}
	if call("POST", "/api/admin/action", actionRequest{Action: "muteRoom", ID: id, Enabled: true}, cookie, "").Code != 200 {
		t.Fatal("mute failed")
	}
	room := call("POST", "/api/room", map[string]string{"id": id, "name": "Lounge TV", "kind": "watch"}, nil, "")
	if !strings.Contains(room.Body.String(), `"muted":true`) {
		t.Fatal("client did not receive mute")
	}
	if a.roomCountLocked() != 1 {
		t.Fatal("mute disconnected room")
	}
	a.roomConnection(id, -1)
	if a.roomCountLocked() != 0 {
		t.Fatal("disconnect left ghost room")
	}
	loaded, err := loadSettings(a.root)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Rooms[id].Muted || loaded.Rooms[id].Name != "Lounge TV" {
		t.Fatal("room state not persisted")
	}
}

func TestCorruptConfigIsNotOverwritten(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "soundstreamer.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettings(root); err == nil {
		t.Fatal("corrupt configuration silently replaced")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "broken" {
		t.Fatal("existing configuration changed")
	}
}

func TestPrepareBroaderVideoFormats(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	for _, fixture := range []struct {
		name, extension, codec string
		audio                  bool
		extra                  []string
	}{
		{"mpeg4-avi", "avi", "mpeg4", true, nil},
		{"vp9-webm-silent", "webm", "libvpx-vp9", false, nil},
		{"hevc-mkv", "mkv", "libx265", true, []string{"-x265-params", "pools=1:frame-threads=1:log-level=error"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			root := t.TempDir()
			source := filepath.Join(root, "input."+fixture.extension)
			args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=20:duration=0.6"}
			if fixture.audio {
				args = append(args, "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=0.6", "-c:a", "pcm_s16le")
			}
			args = append(args, "-c:v", fixture.codec)
			args = append(args, fixture.extra...)
			args = append(args, "-y", source)
			if out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			m, p, err := probeMedia(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			if nativeVideo(p) {
				t.Fatal("incompatible fixture bypassed conversion")
			}
			prepared, err := prepareVideo(ctx, root, m, p, func(float64) {})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.Path != source || !prepared.Transcode {
				t.Fatal("incompatible source was not configured for live encoding")
			}
			chunks := newVideoChunks(ctx)
			defer chunks.close()
			encoded, err := chunks.get(ctx, prepared, 0)
			if err != nil {
				t.Fatal(err)
			}
			if encoded.Codec == "" || !bytes.Contains(encoded.Data, []byte("moof")) {
				t.Fatal("live encoder did not produce fragmented MP4")
			}
			cache, err := chunks.get(ctx, prepared, 0)
			if err != nil || cache != encoded {
				t.Fatal("rooms did not share the encoded chunk")
			}
			if _, err := os.Stat(filepath.Join(root, "soundstreamer-cache")); !os.IsNotExist(err) {
				t.Fatal("live playback created a complete conversion cache")
			}

		})
	}
}

func TestSeekThenRepeatRestartsWholeTrack(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "two-tones.wav")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=1", "-filter_complex", "[0:a][1:a]concat=n=2:v=0:a=1", "-y", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	args := decoderArgs(path, 0.5, 2, true, audioFormat{SampleRate: 48000, Channels: 1})
	args = append(args[:len(args)-1], "-t", "4", "pipe:1")
	pcm, err := exec.CommandContext(ctx, "ffmpeg", args...).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		time      float64
		frequency int
	}{{0.25, 440}, {1.25, 880}, {1.75, 440}, {2.25, 440}, {2.75, 880}} {
		start := int(sample.time * sampleRate)
		crossings := 0
		for n := start; n < start+4800-1; n++ {
			x := int16(binary.LittleEndian.Uint16(pcm[n*2:]))
			y := int16(binary.LittleEndian.Uint16(pcm[(n+1)*2:]))
			if x <= 0 && y > 0 {
				crossings++
			}
		}
		got := crossings * 10
		if got < sample.frequency-10 || got > sample.frequency+10 {
			t.Fatalf("at %.2fs got %dHz, want %dHz: loop repeated the seeked tail", sample.time, got, sample.frequency)
		}
	}
}
