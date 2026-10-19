package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFolderRescanPreservesOtherSourcesAndRefreshesTarget(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	for _, kind := range []string{"music", "video"} {
		t.Run(kind, func(t *testing.T) {
			a := testApplication(t)
			root := t.TempDir()
			target, other, nested := filepath.Join(root, "target"), filepath.Join(root, "other"), filepath.Join(root, "target", "nested")
			for _, folder := range []string{target, other, nested} {
				if err := os.MkdirAll(folder, 0700); err != nil {
					t.Fatal(err)
				}
			}
			extension := ".wav"
			if kind == "video" {
				extension = ".mp4"
			}
			write := func(path, title string) {
				t.Helper()
				args := []string{"-v", "error", "-y", "-f", "lavfi"}
				if kind == "music" {
					args = append(args, "-i", "sine=duration=0.1")
				} else {
					args = append(args, "-i", "color=size=32x32:duration=0.1", "-an", "-c:v", "libx264")
				}
				args = append(args, "-metadata", "title="+title, path)
				if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
			}
			deleted, edited, untouched, subfolder := filepath.Join(target, "deleted"+extension), filepath.Join(target, "edited"+extension), filepath.Join(other, "untouched"+extension), filepath.Join(nested, "nested"+extension)
			write(deleted, "Deleted")
			write(edited, "Before")
			write(untouched, "Other")
			write(subfolder, "Nested")
			if kind == "music" {
				a.config.MusicFolders = []string{target, other, nested}
			} else {
				a.config.VideoFolders = []string{target, other, nested}
			}
			if err := a.action(actionRequest{Action: "rescan"}); err != nil {
				t.Fatal(err)
			}
			waitScan(t, a)
			find := func(path string) media {
				t.Helper()
				for _, m := range a.library {
					if m.Path == path {
						return m
					}
				}
				t.Fatalf("missing %s", path)
				return media{}
			}
			otherMedia, nestedMedia := find(untouched), find(subfolder)
			otherMedia.Title = "Keep cached other"
			nestedMedia.Title = "Keep cached subfolder"
			a.library[otherMedia.ID] = otherMedia
			a.library[nestedMedia.ID] = nestedMedia
			current := find(edited)
			a.config.Current = &current
			a.config.Queue = []queueItem{{Key: "queued", Media: otherMedia}}
			if err := os.Remove(deleted); err != nil {
				t.Fatal(err)
			}
			write(edited, "After")
			added := filepath.Join(target, "added"+extension)
			write(added, "Added")
			if err := a.action(actionRequest{Action: "rescanFolder", Kind: kind, Path: target}); err != nil {
				t.Fatal(err)
			}
			waitScan(t, a)
			if a.scanTotal != 2 || a.scanDone != 2 || len(a.library) != 4 {
				t.Fatalf("unexpected scan scope: %d/%d, library %d", a.scanDone, a.scanTotal, len(a.library))
			}
			for _, m := range a.library {
				if m.Path == deleted {
					t.Fatal("deleted target file remains")
				}
			}
			if find(edited).Title != "After" || find(added).Title != "Added" {
				t.Fatal("target files were not refreshed")
			}
			if find(untouched).Title != "Keep cached other" || find(subfolder).Title != "Keep cached subfolder" {
				t.Fatal("rescan touched another folder or subfolder")
			}
			if a.config.Current != &current || len(a.config.Queue) != 1 || a.config.Queue[0].Key != "queued" {
				t.Fatal("rescan changed playback or queue")
			}
			data, err := os.ReadFile(filepath.Join(a.root, "soundstreamer-library.json"))
			if err != nil {
				t.Fatal(err)
			}
			var cached []media
			if err := json.Unmarshal(data, &cached); err != nil {
				t.Fatal(err)
			}
			if len(cached) != 4 || !bytes.Contains(data, []byte("Keep cached other")) || !bytes.Contains(data, []byte("After")) {
				t.Fatal("merged library cache was not saved")
			}
			if err := a.action(actionRequest{Action: "rescan"}); err != nil {
				t.Fatal(err)
			}
			waitScan(t, a)
			if a.scanTotal != 4 || find(untouched).Title != "Other" || find(subfolder).Title != "Nested" {
				t.Fatal("rescan all did not refresh every source")
			}
		})
	}
}
func TestFolderRescanRejectsUnknownSourceAndPreservesUnavailableFolder(t *testing.T) {
	a := testApplication(t)
	folder := filepath.Join(t.TempDir(), "missing")
	a.config.MusicFolders = []string{folder}
	cached := media{ID: "cached", Path: filepath.Join(folder, "song.mp3"), Kind: "audio", Title: "Cached"}
	a.library[cached.ID] = cached
	if err := a.action(actionRequest{Action: "rescanFolder", Kind: "music", Path: t.TempDir()}); err == nil {
		t.Fatal("unconfigured folder accepted")
	}
	if err := a.action(actionRequest{Action: "rescanFolder", Kind: "video", Path: folder}); err == nil {
		t.Fatal("wrong source type accepted")
	}
	if err := a.action(actionRequest{Action: "rescanFolder", Kind: "music", Path: folder}); err != nil {
		t.Fatal(err)
	}
	waitScan(t, a)
	if a.library[cached.ID] != cached || a.scanErrors == 0 {
		t.Fatal("unavailable source lost cached entries or error")
	}
	r := httptest.NewRequest("POST", "/api/control/action", bytes.NewBufferString(`{"action":"rescanFolder","kind":"music","path":"missing"}`))
	r.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.station.handler().ServeHTTP(response, r)
	if response.Code != 403 {
		t.Fatal("public controls can scan folders")
	}
}
