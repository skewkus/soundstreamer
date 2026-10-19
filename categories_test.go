package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestCategoryMigrationAndFolderRequirements(t *testing.T) {
	root := t.TempDir()
	music, video := t.TempDir(), t.TempDir()
	if err := writeJSON(filepath.Join(root, "soundstreamer.json"), settings{Version: 1, MusicFolders: []string{music}, VideoFolders: []string{video}}); err != nil {
		t.Fatal(err)
	}
	c, err := loadSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.FolderCategories["music"][music] != "music" || c.FolderCategories["video"][video] != "" || len(c.Categories["video"]) != 2 {
		t.Fatal("legacy source migration incorrect")
	}
	a := testApplication(t)
	if err := a.action(actionRequest{Action: "addSource", Kind: "video", Path: video}); err == nil {
		t.Fatal("video source without category accepted")
	}
	if err := a.action(actionRequest{Action: "addSource", Kind: "video", Path: video, Category: "music"}); err == nil {
		t.Fatal("wrong library category accepted")
	}
	if len(a.config.VideoFolders) != 0 || a.scanning {
		t.Fatal("rejected source changed library")
	}
	if err := a.action(actionRequest{Action: "addSource", Kind: "music", Path: music}); err != nil {
		t.Fatal(err)
	}
	waitScan(t, a)
	if a.config.FolderCategories["music"][music] != "music" {
		t.Fatal("music default not assigned")
	}
	if err := a.action(actionRequest{Action: "addSource", Kind: "video", Path: video, Category: "concerts"}); err != nil {
		t.Fatal(err)
	}
	waitScan(t, a)
	saved, err := loadSettings(a.root)
	if err != nil || saved.FolderCategories["video"][video] != "concerts" {
		t.Fatal("source category not saved")
	}
}

func TestCategoriesFilterSearchRenameReassignAndDeleteWithoutScan(t *testing.T) {
	a := testApplication(t)
	concerts, videos, albums := t.TempDir(), t.TempDir(), t.TempDir()
	a.config.VideoFolders = []string{concerts, videos}
	a.config.MusicFolders = []string{albums}
	a.config.FolderCategories["video"][concerts] = "concerts"
	a.config.FolderCategories["video"][videos] = "music-videos"
	a.config.FolderCategories["music"][albums] = "music"
	items := []media{{ID: "concert", Path: filepath.Join(concerts, "Singer live.mp4"), Kind: "video", Title: "Live"}, {ID: "video", Path: filepath.Join(videos, "Singer single.mp4"), Kind: "video", Title: "Single"}, {ID: "audio", Path: filepath.Join(albums, "Singer album.mp3"), Kind: "audio", Title: "Album"}}
	for _, m := range items {
		a.library[m.ID] = m
	}
	a.config.Current = &items[0]
	a.config.Queue = []queueItem{{Key: "queued", Media: items[1]}}
	session := a.session
	get := func(kind, category, query string) []media {
		t.Helper()
		r := httptest.NewRecorder()
		a.station.handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/control/library?kind="+kind+"&category="+category+"&q="+query, nil))
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
		var data struct{ Items []media }
		if err := json.Unmarshal(r.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data.Items
	}
	if len(get("video", "", "Singer")) != 2 || len(get("video", "concerts", "Singer")) != 1 || len(get("video", "concerts", "single")) != 0 || len(get("audio", "music", "Singer")) != 1 {
		t.Fatal("category and search filters are not combined")
	}
	if err := a.action(actionRequest{Action: "renameCategory", Kind: "video", ID: "concerts", Name: "Live Shows"}); err != nil {
		t.Fatal(err)
	}
	if got := get("video", "concerts", ""); len(got) != 1 || got[0].CategoryName != "Live Shows" {
		t.Fatal("rename changed category identity or failed to update")
	}
	if err := a.action(actionRequest{Action: "deleteCategory", Kind: "video", ID: "concerts"}); err == nil {
		t.Fatal("used category deleted")
	}
	if err := a.action(actionRequest{Action: "folderCategory", Kind: "video", Path: concerts, Category: "music-videos"}); err != nil {
		t.Fatal(err)
	}
	if len(get("video", "concerts", "")) != 0 || len(get("video", "music-videos", "")) != 2 {
		t.Fatal("folder reassignment did not update library immediately")
	}
	if err := a.action(actionRequest{Action: "deleteCategory", Kind: "video", ID: "concerts"}); err != nil {
		t.Fatal(err)
	}
	if err := a.action(actionRequest{Action: "deleteCategory", Kind: "video", ID: "music-videos"}); err == nil {
		t.Fatal("last category deleted")
	}
	if err := a.action(actionRequest{Action: "addCategory", Kind: "music", Name: "Albums"}); err != nil {
		t.Fatal(err)
	}
	id := a.config.Categories["music"][1].ID
	if err := a.action(actionRequest{Action: "folderCategory", Kind: "music", Path: albums, Category: id}); err != nil {
		t.Fatal(err)
	}
	if len(get("audio", id, "")) != 1 || len(get("audio", "music", "")) != 0 {
		t.Fatal("custom music category filter failed")
	}
	for _, name := range []string{"albums", "All", ""} {
		if err := a.action(actionRequest{Action: "addCategory", Kind: "music", Name: name}); err == nil {
			t.Fatal("duplicate/reserved/empty name accepted")
		}
	}
	if a.scanning || a.session != session || a.config.Current.ID != "concert" || a.config.Queue[0].Key != "queued" {
		t.Fatal("category settings disrupted scan or playback")
	}
	saved, err := loadSettings(a.root)
	if err != nil || saved.FolderCategories["music"][albums] != id || saved.Categories["music"][1].Name != "Albums" {
		t.Fatal("custom category not persisted")
	}
	request := httptest.NewRequest("POST", "/api/control/action", bytes.NewBufferString(`{"action":"addCategory","kind":"video","name":"Private"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.station.handler().ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("public control changed categories")
	}
}
