package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAddingSourcesDoesNotRescanExistingLibrary(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	for _, kind := range []string{"music", "video"} {
		for _, recursive := range []bool{false, true} {
			name := kind + "-direct"
			if recursive {
				name = kind + "-recursive"
			}
			t.Run(name, func(t *testing.T) {
				a := testApplication(t)
				musicRoot, videoRoot, newRoot := t.TempDir(), t.TempDir(), t.TempDir()
				a.config.MusicFolders = []string{musicRoot}
				a.config.VideoFolders = []string{videoRoot}
				music := media{ID: "cached-music", Path: filepath.Join(musicRoot, "old.mp3"), Title: "Keep cached music", Kind: "audio", Duration: 60}
				video := media{ID: "cached-video", Path: filepath.Join(videoRoot, "old.mp4"), Title: "Keep cached video", Kind: "video", Duration: 120}
				a.library[music.ID] = music
				a.library[video.ID] = video
				// Invalid new files in existing sources make an accidental rescan observable:
				// the cached entries disappear and scanErrors/counts increase.
				for _, path := range []string{music.Path, video.Path} {
					if err := os.WriteFile(path, []byte("old source must not be probed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				a.config.Current = &music
				a.config.Queue = []queueItem{{Key: "keep-queue", Media: video}}
				current, session := a.config.Current, a.session
				a.scanTotal = 123
				a.scanDone = 123
				if err := a.action(actionRequest{Action: "addCategory", Kind: kind, Name: "Test"}); err != nil {
					t.Fatal(err)
				}
				if a.scanning || a.scanTotal != 123 || a.scanDone != 123 {
					t.Fatal("category creation triggered a scan")
				}
				category := a.config.Categories[kind][len(a.config.Categories[kind])-1].ID
				nested := filepath.Join(newRoot, "nested")
				if err := os.Mkdir(nested, 0700); err != nil {
					t.Fatal(err)
				}
				extension := ".wav"
				if kind == "video" {
					extension = ".mp4"
				}
				for _, folder := range []string{newRoot, nested} {
					args := []string{"-v", "error", "-f", "lavfi"}
					if kind == "music" {
						args = append(args, "-i", "sine=duration=0.1")
					} else {
						args = append(args, "-i", "color=size=32x32:duration=0.1", "-an", "-c:v", "libx264")
					}
					args = append(args, filepath.Join(folder, "new"+extension))
					if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
						t.Fatalf("fixture: %v %s", err, out)
					}
				}
				if err := a.action(actionRequest{Action: "addSource", Kind: kind, Path: newRoot, Category: category, IncludeSubfolders: recursive}); err != nil {
					t.Fatal(err)
				}
				waitScan(t, a)
				expected := 1
				if recursive {
					expected = 2
				}
				if a.scanTotal != expected || a.scanDone != expected || a.scanErrors != 0 || len(a.library) != 2+expected {
					t.Fatalf("adding source scanned unrelated files: total=%d done=%d errors=%d library=%d", a.scanTotal, a.scanDone, a.scanErrors, len(a.library))
				}
				if a.library[music.ID] != music || a.library[video.ID] != video || a.config.Current != current || a.session != session || len(a.config.Queue) != 1 || a.config.Queue[0].Key != "keep-queue" {
					t.Fatal("adding a source changed cached media or playback")
				}
				for id, m := range a.library {
					if id != music.ID && id != video.ID {
						c, _ := a.mediaCategoryLocked(m)
						if c != category {
							t.Fatal("new source category was not applied")
						}
					}
				}
				data, err := os.ReadFile(filepath.Join(a.root, "soundstreamer-library.json"))
				if err != nil {
					t.Fatal(err)
				}
				var cached []media
				if err := json.Unmarshal(data, &cached); err != nil || len(cached) != 2+expected {
					t.Fatal("merged library cache was not saved")
				}
				if err := a.action(actionRequest{Action: "removeSource", Kind: kind, Path: newRoot}); err != nil {
					t.Fatal(err)
				}
				waitScan(t, a)
				if a.scanTotal != 0 || a.scanDone != 0 || a.scanErrors != 0 || len(a.library) != 2 || a.library[music.ID] != music || a.library[video.ID] != video {
					t.Fatal("removing source rescanned unrelated sources or lost their cache")
				}
				// Only an explicit Rescan all should probe the deliberately invalid old files.
				if err := a.action(actionRequest{Action: "rescan"}); err != nil {
					t.Fatal(err)
				}
				waitScan(t, a)
				if a.scanTotal != 2 || a.scanErrors != 2 {
					t.Fatal("Rescan all stopped scanning every configured source")
				}
			})
		}
	}
}
