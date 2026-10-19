package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestTitleSourceChangesCachedLibraryQueueAndStreamWithoutRescan(t *testing.T) {
	a := testApplication(t)
	if a.config.TitleSource != "filename" {
		t.Fatal("filename titles must be the default")
	}
	song := media{ID: "song", Path: filepath.Join(t.TempDir(), "Artist - My song.live.MP3"), Title: "Embedded song", Artist: "Artist", Kind: "audio", Duration: 30}
	video := media{ID: "video", Path: filepath.Join(t.TempDir(), "My film.final.mp4"), Title: "Embedded film", Kind: "video", Duration: 30}
	a.library[song.ID] = song
	a.library[video.ID] = video
	a.config.Current = &song
	a.config.Queue = []queueItem{{Key: "queued", Media: video}}
	a.config.Playlists["saved"] = []media{song, video}
	originalSession := a.session
	library := func(kind, query string) []media {
		t.Helper()
		r := httptest.NewRecorder()
		a.station.handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/control/library?kind="+kind+"&q="+query, nil))
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
		var data struct{ Items []media }
		if err := json.Unmarshal(r.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data.Items
	}
	if got := library("audio", ""); len(got) != 1 || got[0].Title != "Artist - My song.live" {
		t.Fatalf("filename not displayed: %+v", got)
	}
	if got := library("video", ""); len(got) != 1 || got[0].Title != "My film.final" {
		t.Fatal("video filename not displayed")
	}
	if len(library("audio", "Embedded")) != 0 {
		t.Fatal("filename mode searched hidden metadata title")
	}
	state := a.state()
	if state["current"].(*media).Title != "Artist - My song.live" || state["queue"].([]queueItem)[0].Media.Title != "My film.final" || a.clock()["title"] != "Artist - My song.live" {
		t.Fatal("displayed titles inconsistent")
	}
	if err := a.action(actionRequest{Action: "titleSource", Name: "metadata"}); err != nil {
		t.Fatal(err)
	}
	if a.scanning || a.libraryRevision != 1 || a.session != originalSession {
		t.Fatal("title switch scanned or disrupted playback")
	}
	if library("audio", "")[0].Title != "Embedded song" || a.state()["queue"].([]queueItem)[0].Media.Title != "Embedded film" || a.clock()["title"] != "Embedded song" {
		t.Fatal("metadata titles not restored")
	}
	if a.library[song.ID].Title != song.Title || a.config.Current.Title != song.Title || a.config.Playlists["saved"][0].Title != song.Title {
		t.Fatal("title switch overwrote cached metadata")
	}
	saved, err := loadSettings(a.root)
	if err != nil || saved.TitleSource != "metadata" {
		t.Fatal("title preference not saved")
	}
	if err := a.action(actionRequest{Action: "titleSource", Name: "filename"}); err != nil {
		t.Fatal(err)
	}
	if err := a.action(actionRequest{Action: "loadPlaylist", Name: "saved"}); err != nil {
		t.Fatal(err)
	}
	queue := a.state()["queue"].([]queueItem)
	if queue[1].Media.Title != "Artist - My song.live" {
		t.Fatal("playlist titles ignored preference")
	}
	if err := a.action(actionRequest{Action: "titleSource", Name: "invalid"}); err == nil {
		t.Fatal("invalid title source accepted")
	}
	request := httptest.NewRequest("POST", "/api/control/action", bytes.NewBufferString(`{"action":"titleSource","name":"metadata"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.station.handler().ServeHTTP(response, request)
	if response.Code != 403 || a.config.TitleSource != "filename" {
		t.Fatal("public control changed settings")
	}
}
