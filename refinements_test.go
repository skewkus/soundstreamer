package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitScan(t *testing.T, a *application) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		scanning := a.scanning
		a.mu.Unlock()
		if !scanning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("library scan did not finish")
}

func TestMultipleSourcesDedupeRemoveAndPersist(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	a := testApplication(t)
	root := t.TempDir()
	one, two := filepath.Join(root, "one"), filepath.Join(root, "two")
	for _, folder := range []string{one, two} {
		if err := os.Mkdir(folder, 0700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=duration=0.1", filepath.Join(folder, "track.wav")).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	add := func(path string) {
		t.Helper()
		if err := a.action(actionRequest{Action: "addSource", Kind: "music", Path: path}); err != nil {
			t.Fatal(err)
		}
		waitScan(t, a)
	}
	add(root)
	if len(a.library) != 0 {
		t.Fatal("parent folder included files from subfolders")
	}
	add(one)
	if len(a.library) != 1 {
		t.Fatal("explicitly added subfolder was not scanned")
	}
	add(two)
	add(one)
	if len(a.config.MusicFolders) != 3 || len(a.library) != 2 {
		t.Fatal("multiple folders or duplicate handling failed")
	}
	add(root)
	if len(a.library) != 2 {
		t.Fatal("overlapping folders duplicated tracks")
	}
	if err := a.action(actionRequest{Action: "removeSource", Kind: "music", Path: root}); err != nil {
		t.Fatal(err)
	}
	waitScan(t, a)
	if err := a.action(actionRequest{Action: "removeSource", Kind: "music", Path: one}); err != nil {
		t.Fatal(err)
	}
	waitScan(t, a)
	if len(a.library) != 1 || len(a.config.MusicFolders) != 1 {
		t.Fatal("removing source did not rebuild library")
	}
	if _, err := os.Stat(filepath.Join(one, "track.wav")); err != nil {
		t.Fatal("removing source changed media files")
	}
	restored, err := loadSettings(a.root)
	if err != nil || len(restored.MusicFolders) != 1 || restored.MusicFolders[0] != two {
		t.Fatalf("sources not persisted: %+v %v", restored, err)
	}
}

func TestThemePersistenceAndAdminOnly(t *testing.T) {
	a := testApplication(t)
	if a.config.Theme != "green" {
		t.Fatal("default theme")
	}
	for _, theme := range []string{"blue", "red", "purple", "amber", "teal", "green"} {
		if err := a.action(actionRequest{Action: "theme", Name: theme}); err != nil {
			t.Fatal(err)
		}
		saved, err := loadSettings(a.root)
		if err != nil || saved.Theme != theme {
			t.Fatal("theme not persisted")
		}
	}
	if err := a.action(actionRequest{Action: "theme", Name: "invalid"}); err == nil {
		t.Fatal("invalid theme accepted")
	}
	h := a.station.handler()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/control/action", strings.NewReader(`{"action":"theme","name":"red"}`)))
	if response.Code == 200 || a.config.Theme != "green" {
		t.Fatal("public control changed theme")
	}
	response = httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/theme", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"theme":"green"`) {
		t.Fatal("theme unavailable to listeners")
	}
}
