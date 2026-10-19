package main

import (
	"path/filepath"
	"strings"
)

type librarySource struct {
	path       string
	extensions map[string]bool
	recursive  bool
}

func folderContainsFile(folder, path string) bool {
	folderAbs, err := filepath.Abs(folder)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(folderAbs, pathAbs)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (s librarySource) owns(path string) bool {
	return s.extensions[strings.ToLower(filepath.Ext(path))] && (sameLibraryFolder(filepath.Dir(path), s.path) || (s.recursive && folderContainsFile(s.path, path)))
}
func (a *application) librarySourcesLocked() []librarySource {
	sources := []librarySource{}
	for _, kind := range []string{"music", "video"} {
		folders, extensions := a.config.MusicFolders, musicExtensions
		if kind == "video" {
			folders, extensions = a.config.VideoFolders, videoExtensions
		}
		for _, path := range folders {
			sources = append(sources, librarySource{path, extensions, a.config.FolderRecursive[kind][path]})
		}
	}
	return sources
}
func (a *application) recursiveSnapshotLocked() map[string]map[string]bool {
	snapshot := map[string]map[string]bool{}
	for _, kind := range []string{"music", "video"} {
		snapshot[kind] = map[string]bool{}
		for path, enabled := range a.config.FolderRecursive[kind] {
			snapshot[kind][path] = enabled
		}
	}
	return snapshot
}

func containsConfiguredFolder(folders []string, path string) bool {
	if path == "" {
		return false
	}
	for _, folder := range folders {
		if sameLibraryFolder(folder, path) {
			return true
		}
	}
	return false
}
