package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOptionalSubfoldersScanPersistAndCategoryInheritance(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	for _, kind := range []string{"music", "video"} {
		t.Run(kind, func(t *testing.T) {
			a := testApplication(t)
			root := t.TempDir()
			nested := filepath.Join(root, "sub")
			deep := filepath.Join(nested, "deep")
			other := t.TempDir()
			if err := os.MkdirAll(deep, 0700); err != nil {
				t.Fatal(err)
			}
			ext := ".wav"
			category := "music"
			if kind == "video" {
				ext = ".mp4"
				category = "concerts"
			}
			files := []string{filepath.Join(root, "root"+ext), filepath.Join(nested, "sub"+ext), filepath.Join(deep, "deep"+ext), filepath.Join(other, "other"+ext)}
			for _, path := range files {
				args := []string{"-v", "error", "-f", "lavfi"}
				if kind == "music" {
					args = append(args, "-i", "sine=duration=0.1")
				} else {
					args = append(args, "-i", "color=size=32x32:duration=0.1", "-an", "-c:v", "libx264")
				}
				if out, err := exec.Command("ffmpeg", append(args, path)...).CombinedOutput(); err != nil {
					t.Fatalf("%v %s", err, out)
				}
			}
			for _, path := range []string{filepath.Join(root, "notes.txt"), filepath.Join(nested, "details.nfo"), filepath.Join(deep, "cover.jpg")} {
				if err := os.WriteFile(path, []byte("Not media"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			add := func(path string, recursive bool, category string) {
				t.Helper()
				if err := a.action(actionRequest{Action: "addSource", Kind: kind, Path: path, Category: category, IncludeSubfolders: recursive}); err != nil {
					t.Fatal(err)
				}
				waitScan(t, a)
			}
			toggle := func(path string, enabled bool) {
				t.Helper()
				if err := a.action(actionRequest{Action: "folderRecursion", Kind: kind, Path: path, Enabled: enabled}); err != nil {
					t.Fatal(err)
				}
				waitScan(t, a)
			}
			find := func(path string) media {
				t.Helper()
				for _, m := range a.library {
					if m.Path == path {
						return m
					}
				}
				t.Fatalf("missing file %s", path)
				return media{}
			}
			add(root, false, category)
			if len(a.library) != 1 {
				t.Fatal("subfolders included by default")
			}
			add(other, false, category)
			toggle(root, true)
			if len(a.library) != 4 || a.scanTotal != 3 || a.scanErrors != 0 {
				t.Fatal("recursive folder scan missed descendants or scanned unrelated source")
			}
			if got := a.displayMediaLocked(find(files[2])); got.Category != category {
				t.Fatal("descendant did not inherit root category")
			}
			if err := a.action(actionRequest{Action: "addCategory", Kind: kind, Name: "Nested category"}); err != nil {
				t.Fatal(err)
			}
			categories := a.config.Categories[kind]
			nestedCategory := categories[len(categories)-1].ID
			add(nested, false, nestedCategory)
			if len(a.library) != 4 || a.displayMediaLocked(find(files[1])).Category != nestedCategory || a.displayMediaLocked(find(files[2])).Category != category {
				t.Fatal("overlap deduplication or category precedence incorrect")
			}
			if err := os.Remove(files[1]); err != nil {
				t.Fatal(err)
			}
			if err := a.action(actionRequest{Action: "rescanFolder", Kind: kind, Path: root}); err != nil {
				t.Fatal(err)
			}
			waitScan(t, a)
			if len(a.library) != 3 {
				t.Fatal("recursive scan kept a deleted file from an overlapping source")
			}
			toggle(root, false)
			if len(a.library) != 2 {
				t.Fatal("disabling recursion kept imported descendants")
			}
			find(files[0])
			find(files[3])
			toggle(nested, true)
			if len(a.library) != 3 || a.displayMediaLocked(find(files[2])).Category != nestedCategory {
				t.Fatal("separately configured subfolder recursion failed")
			}
			saved, err := loadSettings(a.root)
			if err != nil || saved.FolderRecursive[kind][root] || !saved.FolderRecursive[kind][nested] {
				t.Fatal("recursive choices not saved")
			}
			toggle(root, true)
			toggle(root, false)
			if len(a.library) != 3 {
				t.Fatal("disabling parent recursion removed separately configured nested source")
			}
		})
	}
}
