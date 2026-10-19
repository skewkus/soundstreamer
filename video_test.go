package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestVideoRangeAndState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.mp4")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	a := testApplication(t)
	a.video = &videoSource{Path: path, Duration: 12}
	a.config.Current = &media{Path: path, Duration: 12, Kind: "video", Title: "movie"}
	h := a.station.handler()
	request := httptest.NewRequest(http.MethodGet, "/api/video?session="+a.session, nil)
	request.Header.Set("Range", "bytes=2-5")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 206 || response.Body.String() != "2345" || response.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("range response: %d %q %v", response.Code, response.Body.String(), response.Header())
	}
	clock := httptest.NewRecorder()
	h.ServeHTTP(clock, httptest.NewRequest(http.MethodGet, "/api/clock", nil))
	var state struct {
		Mode    string
		Session string
		Video   struct {
			URL      string
			Duration float64
		}
	}
	if err := json.Unmarshal(clock.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Mode != "video" || state.Session == "" || state.Video.Duration != 12 || state.Video.URL != "/api/video" {
		t.Fatalf("invalid video state: %+v", state)
	}
	for _, route := range []string{"/stream", "/video.js", "/video.css"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
		if response.Code != 200 {
			t.Errorf("%s unavailable", route)
		}
	}
	root := httptest.NewRecorder()
	h.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Header().Get("Location") != "/stream" {
		t.Fatal("video landing page incorrect")
	}
	plain := testApplication(t).station.handler()
	missing := httptest.NewRecorder()
	plain.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/video", nil))
	if missing.Code != 404 {
		t.Fatal("audio-only station exposed video")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing = httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/video", nil))
	if missing.Code != 503 {
		t.Fatal("deleted video did not return useful error")
	}
}
