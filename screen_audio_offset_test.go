package main

import (
	"bytes"
	"math"
	"net/http/httptest"
	"testing"
	"time"
)

func TestScreenAudioOffsetPersistenceScopeAndValidation(t *testing.T) {
	a := testApplication(t)
	if a.config.ScreenAudioOffsetMs != 0 {
		t.Fatal("offset must default to zero")
	}
	session := a.session
	for _, offset := range []int{-3000, -1000, 0, 1000, 3000} {
		if err := a.action(actionRequest{Action: "screenAudioOffset", To: offset}); err != nil {
			t.Fatal(err)
		}
		saved, err := loadSettings(a.root)
		if err != nil || saved.ScreenAudioOffsetMs != offset {
			t.Fatal("offset not saved")
		}
		if a.session != session {
			t.Fatal("offset setting disrupted file or audio-only playback")
		}
	}
	for _, offset := range []int{-3001, 3001} {
		if err := a.action(actionRequest{Action: "screenAudioOffset", To: offset}); err == nil {
			t.Fatal("invalid offset accepted")
		}
	}
	request := httptest.NewRequest("POST", "/api/control/action", bytes.NewBufferString(`{"action":"screenAudioOffset","to":1000}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.station.handler().ServeHTTP(response, request)
	if response.Code != 403 || a.config.ScreenAudioOffsetMs != 3000 {
		t.Fatal("public controls changed screen calibration")
	}
}

func TestScreenAudioOffsetSignedTimelineAndSessionReset(t *testing.T) {
	for _, buffer := range []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond, 5 * time.Second} {
		for _, offset := range []int{-3000, -1000, 0, 1000, 3000} {
			a := testApplication(t)
			a.station.delay = buffer
			a.pcScreen = &desktopStream{}
			a.pcAudio = true
			a.playing = true
			at := a.station.origin.Add(2 * time.Second)
			a.screenAnchorBaseMs = float64(at.Add(buffer).Sub(a.station.origin)) / float64(time.Millisecond)
			a.anchorMs = a.screenAnchorBaseMs
			stream, generation, session := a.pcScreen, a.generation, a.session
			listener := make(chan []byte, 1)
			a.station.clients[listener] = struct{}{}
			if err := a.action(actionRequest{Action: "screenAudioOffset", To: offset}); err != nil {
				t.Fatal(err)
			}
			audioAt := float64(a.screenAudioPlayoutLocked(at)) / float64(time.Millisecond)
			if math.Abs(audioAt-a.anchorMs-float64(offset)) > .001 {
				t.Fatalf("incorrect lip-sync offset for buffer %v, offset %d", buffer, offset)
			}
			if audioAt-float64(at.Sub(a.station.origin))/float64(time.Millisecond) < 499.99 {
				t.Fatal("audio advance left insufficient delivery buffer")
			}
			if a.pcScreen != stream || a.generation != generation || !a.playing {
				t.Fatal("offset restarted capture")
			}
			if offset != 0 {
				if a.session == session {
					t.Fatal("queued audio was not reset")
				}
				if _, open := <-listener; open {
					t.Fatal("old audio session left open")
				}
			} else if a.session != session {
				t.Fatal("unchanged offset reset playback")
			}
			session = a.session
			if err := a.action(actionRequest{Action: "screenAudioOffset", To: offset}); err != nil {
				t.Fatal(err)
			}
			if a.session != session {
				t.Fatal("reapplying same offset reset playback")
			}
			a.playing = false
		}
	}
}
