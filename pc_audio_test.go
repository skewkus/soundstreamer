package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func waitPlaybackState(t *testing.T, a *application, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		ready := predicate()
		a.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("playback state did not settle")
}

func TestPCAudioContinuousSharedTimingAndStop(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	a := testApplication(t)
	a.pcAudioSource = func(ctx context.Context) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-re", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-ac", "2", "-c:a", "pcm_f32le", "-f", "wav", "pipe:1"), nil
	}
	selected := media{ID: "selected", Title: "Selected", Kind: "audio", Duration: 30}
	a.config.Current = &selected
	a.position = 7
	a.config.Queue = []queueItem{{Key: "next", Media: selected}}
	if err := a.action(actionRequest{Action: "pcAudio"}); err != nil {
		t.Fatal(err)
	}
	waitPlaybackState(t, a, func() bool { return a.playing && !a.loading })
	if got := a.clock(); got["mode"] != "pc" || got["title"] != "PC audio" {
		t.Fatalf("incorrect live descriptor: %+v", got)
	}
	left, right := make(chan []byte, 25), make(chan []byte, 25)
	a.station.mu.Lock()
	a.station.clients[left] = struct{}{}
	a.station.clients[right] = struct{}{}
	a.station.mu.Unlock()
	previous := 0.0
	for i := 0; i < 8; i++ {
		var one, two []byte
		select {
		case one = <-left:
		case <-time.After(3 * time.Second):
			t.Fatal("no live audio")
		}
		select {
		case two = <-right:
		case <-time.After(3 * time.Second):
			t.Fatal("no audio in second room")
		}
		if !bytes.Equal(one, two) {
			t.Fatal("rooms received different samples or timestamps")
		}
		timestamp := math.Float64frombits(binary.LittleEndian.Uint64(one[:8]))
		if i > 0 && timestamp-previous < 19.99 || i > 0 && timestamp-previous > 20.01 {
			t.Fatalf("unstable live timeline: %.6f", timestamp-previous)
		}
		previous = timestamp
	}
	savedLive, saveErr := loadSettings(a.root)
	if saveErr != nil || savedLive.Position != 7 {
		t.Fatal("live capture did not preserve restart position")
	}
	if err := a.action(actionRequest{Action: "seek", Position: 10}); err == nil {
		t.Fatal("live seek accepted")
	}
	if err := a.action(actionRequest{Action: "repeat", Repeat: "one"}); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	if !a.pcAudio || len(a.config.Queue) != 1 {
		t.Fatal("capture or queue changed")
	}
	a.mu.Unlock()
	if err := a.action(actionRequest{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	a.workers.Wait()
	if a.playing || a.pcAudio || a.loading {
		t.Fatal("capture did not stop")
	}
	if a.position != 7 || a.config.Current.ID != "selected" || len(a.config.Queue) != 1 {
		t.Fatal("capture lost file selection, position or queue")
	}
	saved, err := loadSettings(a.root)
	if err != nil || saved.Position != 7 {
		t.Fatal("capture damaged saved position")
	}
	if err := a.action(actionRequest{Action: "freeze", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatchAction(actionRequest{Action: "pcAudio"}, true); err != errFrozen {
		t.Fatal("frozen public controls started capture")
	}
}

func TestPCAudioSetupFailureLeavesPlaybackAlone(t *testing.T) {
	a := testApplication(t)
	a.pcAudioSource = func(ctx context.Context) (*exec.Cmd, error) { return nil, fmt.Errorf("no audio device") }
	previous := a.session
	if err := a.action(actionRequest{Action: "pcAudio"}); err == nil {
		t.Fatal("missing source accepted")
	}
	if a.session != previous || a.pcAudio {
		t.Fatal("setup failure changed playback")
	}
	if runtime.GOOS != "windows" {
		if _, err := pcAudioCommand(context.Background()); err == nil {
			t.Fatal("unsupported platform accepted")
		}
	}
}
func TestPCAudioChildFailureIsReported(t *testing.T) {
	a := testApplication(t)
	a.pcAudioSource = func(ctx context.Context) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "missing-soundstreamer-capture-command"), nil
	}
	if err := a.action(actionRequest{Action: "pcAudio"}); err != nil {
		t.Fatal(err)
	}
	waitPlaybackState(t, a, func() bool { return a.lastError != "" && !a.loading && !a.playing })
	a.workers.Wait()
}
