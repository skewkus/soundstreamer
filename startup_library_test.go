package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartupReusesSavedLibraryIncludingEmptyCache(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "populated"
		if empty {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			folder := t.TempDir()
			if err := writeJSON(filepath.Join(root, "soundstreamer.json"), settings{Version: 1, MusicFolders: []string{folder}, Volume: 1}); err != nil {
				t.Fatal(err)
			}
			cached := []media{}
			if !empty {
				cached = append(cached, media{ID: "cached", Path: filepath.Join(folder, "cached.mp3"), Title: "Saved title", Kind: "audio"})
			}
			cachePath := filepath.Join(root, "soundstreamer-library.json")
			if err := writeJSON(cachePath, cached); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(cachePath)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			a, err := newApplication(ctx, root, 1500*time.Millisecond)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			t.Cleanup(func() { cancel(); a.shutdown() })
			if !a.libraryCacheLoaded || len(a.library) != len(cached) {
				t.Fatal("saved library not restored")
			}
			a.background()
			// Startup starts scans synchronously; if an empty scan completes before this
			// check its revision records the unwanted refresh.
			a.mu.Lock()
			scanning, revision, count := a.scanning, a.libraryRevision, len(a.library)
			a.mu.Unlock()
			if scanning || revision != 0 || count != len(cached) {
				t.Fatal("startup rescanned a valid cache")
			}
			cancel()
			a.shutdown()
			after, err := os.ReadFile(cachePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("restart changed cached metadata")
			}
		})
	}
}

func TestStartupBuildsLibraryWhenCacheMissingOrInvalid(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		root := t.TempDir()
		folder := t.TempDir()
		if err := writeJSON(filepath.Join(root, "soundstreamer.json"), settings{Version: 1, MusicFolders: []string{folder}, Volume: 1}); err != nil {
			t.Fatal(err)
		}
		cachePath := filepath.Join(root, "soundstreamer-library.json")
		if corrupt {
			if err := os.WriteFile(cachePath, []byte("{invalid"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		a, err := newApplication(ctx, root, 1500*time.Millisecond)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Cleanup(func() { cancel(); a.shutdown() })
		a.background()
		waitScan(t, a)
		a.mu.Lock()
		revision := a.libraryRevision
		a.mu.Unlock()
		if revision != 1 {
			t.Fatal("startup did not initialize a missing/invalid cache")
		}
		data, err := os.ReadFile(cachePath)
		if err != nil || string(data) != "[]\n" {
			t.Fatal("initial library cache was not saved")
		}
	}
}
