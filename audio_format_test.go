package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func testAudioApplication(t *testing.T, format audioFormat) *application {
	t.Helper()
	root := t.TempDir()
	if err := writeJSON(filepath.Join(root, "soundstreamer.json"), settings{Version: 1, Volume: 1, AudioChannels: format.Channels, AudioSampleRate: format.SampleRate}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := newApplication(ctx, root, 1500*time.Millisecond)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); a.shutdown() })
	return a
}

func TestAudioSettingsDefaultsPersistenceAndRestart(t *testing.T) {
	a := testApplication(t)
	if a.audio != (audioFormat{48000, 2}) {
		t.Fatal("new installation did not default to stereo/48 kHz")
	}
	session := a.session
	for _, rate := range []int{48000, 44100, 32000, 24000} {
		for _, channels := range []int{1, 2} {
			if err := a.action(actionRequest{Action: "audioSettings", AudioChannels: channels, AudioSampleRate: rate}); err != nil {
				t.Fatal(err)
			}
			saved, err := loadSettings(a.root)
			if err != nil || saved.AudioChannels != channels || saved.AudioSampleRate != rate {
				t.Fatal("audio settings not persisted")
			}
			if a.audio != (audioFormat{48000, 2}) || a.session != session {
				t.Fatal("saving audio settings changed running format or session")
			}
		}
	}
	for _, request := range []actionRequest{
		{Action: "audioSettings", AudioChannels: 0, AudioSampleRate: 48000},
		{Action: "audioSettings", AudioChannels: 3, AudioSampleRate: 48000},
		{Action: "audioSettings", AudioChannels: 2, AudioSampleRate: 96000},
	} {
		if a.action(request) == nil {
			t.Fatal("accepted invalid audio format")
		}
	}
	if err := a.action(actionRequest{Action: "audioSettings", AudioChannels: 1, AudioSampleRate: 44100}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	restarted, err := newApplication(ctx, a.root, 1500*time.Millisecond)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); restarted.shutdown() }()
	if restarted.audio != (audioFormat{44100, 1}) {
		t.Fatal("restart did not apply saved format")
	}
	if restarted.clock()["sampleRate"] != 44100 || restarted.clock()["channels"] != 1 {
		t.Fatal("clock does not advertise active format")
	}
	req := httptest.NewRequest("POST", "/api/control/action", bytes.NewBufferString(`{"action":"audioSettings","audioChannels":2,"audioSampleRate":48000}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.station.handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("public API changed audio format")
	}
	if a.dispatchAction(actionRequest{Action: "audioSettings", AudioChannels: 2, AudioSampleRate: 48000}, true) == nil {
		t.Fatal("public dispatcher changed format")
	}
	// Old configuration files lacking the new fields also migrate to stereo/48 kHz.
	root := t.TempDir()
	if err := writeJSON(filepath.Join(root, "soundstreamer.json"), map[string]any{"version": 1}); err != nil {
		t.Fatal(err)
	}
	migrated, err := loadSettings(root)
	if err != nil || migrated.AudioChannels != 2 || migrated.AudioSampleRate != 48000 {
		t.Fatal("legacy defaults were not migrated")
	}
}

func TestAudioFormatsHTTPPacketsAndIdleTiming(t *testing.T) {
	for _, rate := range []int{48000, 44100, 32000, 24000} {
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%d-%d", rate, channels), func(t *testing.T) {
				format := audioFormat{rate, channels}
				a := testAudioApplication(t, format)
				a.background()
				server := httptest.NewServer(a.station.handler())
				defer server.Close()
				client := &http.Client{Timeout: 3 * time.Second}
				resp, err := client.Get(server.URL + "/api/audio")
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.Header.Get("X-Audio-Channels") != strconv.Itoa(channels) || resp.Header.Get("X-Audio-Sample-Rate") != strconv.Itoa(rate) {
					t.Fatal("audio response advertises wrong format")
				}
				var previous float64
				for n := 0; n < 3; n++ {
					packet := make([]byte, 12+format.chunkBytes())
					if _, err := io.ReadFull(resp.Body, packet); err != nil {
						t.Fatal(err)
					}
					if int(binary.LittleEndian.Uint32(packet[8:12])) != format.chunkFrames() {
						t.Fatal("header counts samples instead of frames")
					}
					if !bytes.Equal(packet[12:], make([]byte, format.chunkBytes())) {
						t.Fatal("idle packet not silent")
					}
					at := math.Float64frombits(binary.LittleEndian.Uint64(packet))
					if float64(format.chunkFrames())/float64(rate) != .02 || (n > 0 && at <= previous) {
						t.Fatal("idle packet duration or timestamp progression invalid")
					}
					previous = at
				}
			})
		}
	}
}

func TestAudioDecoderPreservesStereoAndResamples(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "stereo.wav")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "aevalsrc=0.25|-0.5:s=48000:d=0.3", "-c:a", "pcm_s16le", "-y", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	for _, rate := range []int{48000, 44100, 32000, 24000} {
		for _, channels := range []int{1, 2} {
			format := audioFormat{rate, channels}
			pcm, err := exec.CommandContext(ctx, "ffmpeg", decoderArgs(path, 0, .3, false, format)...).Output()
			if err != nil {
				t.Fatal(err)
			}
			if len(pcm) != rate*3/10*channels*2 {
				t.Fatalf("incorrect PCM duration for %v: %d bytes", format, len(pcm))
			}
			index := format.chunkBytes() * 3
			if channels == 2 {
				left, right := int16(binary.LittleEndian.Uint16(pcm[index:])), int16(binary.LittleEndian.Uint16(pcm[index+2:]))
				if left < 7000 || left > 9000 || right > -15000 || right < -18000 {
					t.Fatalf("stereo separation lost: %d/%d", left, right)
				}
			} else if int16(binary.LittleEndian.Uint16(pcm[index:])) >= 0 {
				t.Fatal("mono was not downmixed from both channels")
			}
			silence, err := exec.CommandContext(ctx, "ffmpeg", silentDecoderArgs(.06, false, format)...).Output()
			if err != nil {
				t.Fatal(err)
			}
			if len(silence) != format.chunkBytes()*3 || !bytes.Equal(silence, make([]byte, len(silence))) {
				t.Fatal("silent-video PCM has wrong size or format")
			}
		}
	}
}

func TestPCAudioUsesConfiguredStereoAndRate(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	for _, format := range []audioFormat{{48000, 2}, {44100, 2}, {32000, 1}, {24000, 2}} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			a := testAudioApplication(t, format)
			a.pcAudioSource = func(ctx context.Context) (*exec.Cmd, error) {
				return exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-re", "-f", "lavfi", "-i", "aevalsrc=0.25|-0.5:s=48000", "-c:a", "pcm_f32le", "-f", "wav", "pipe:1"), nil
			}
			if err := a.startPCAudio(); err != nil {
				t.Fatal(err)
			}
			waitPlaybackState(t, a, func() bool { return a.playing || a.lastError != "" })
			a.mu.Lock()
			failure := a.lastError
			a.mu.Unlock()
			if failure != "" {
				t.Fatal(failure)
			}
			packets := make(chan []byte, 25)
			a.station.mu.Lock()
			a.station.clients[packets] = struct{}{}
			a.station.mu.Unlock()
			var previous float64
			for n := 0; n < 4; n++ {
				var p []byte
				select {
				case p = <-packets:
				case <-time.After(5 * time.Second):
					t.Fatal("capture produced no PCM")
				}
				if len(p) != 12+format.chunkBytes() || int(binary.LittleEndian.Uint32(p[8:])) != format.chunkFrames() {
					t.Fatal("capture PCM format incorrect")
				}
				if format.Channels == 2 && n > 0 {
					left, right := int16(binary.LittleEndian.Uint16(p[12:])), int16(binary.LittleEndian.Uint16(p[14:]))
					if left <= 0 || right >= 0 {
						t.Fatalf("PC capture lost left/right separation: %d/%d", left, right)
					}
				}
				at := math.Float64frombits(binary.LittleEndian.Uint64(p))
				if n > 0 && math.Abs(at-previous-20) > .01 {
					t.Fatal("capture timing changed with format")
				}
				previous = at
			}
		})
	}
}
