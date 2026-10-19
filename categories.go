package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

type libraryCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *settings) initCategories() {
	if c.FolderRecursive == nil {
		c.FolderRecursive = map[string]map[string]bool{}
	}
	for _, kind := range []string{"music", "video"} {
		if c.FolderRecursive[kind] == nil {
			c.FolderRecursive[kind] = map[string]bool{}
		}
	}
	if c.Categories == nil {
		c.Categories = map[string][]libraryCategory{}
	}
	if len(c.Categories["music"]) == 0 {
		c.Categories["music"] = []libraryCategory{{"music", "Music"}}
	}
	if len(c.Categories["video"]) == 0 {
		c.Categories["video"] = []libraryCategory{{"concerts", "Concerts"}, {"music-videos", "Music Videos"}}
	}
	if c.FolderCategories == nil {
		c.FolderCategories = map[string]map[string]string{}
	}
	for _, kind := range []string{"music", "video"} {
		if c.FolderCategories[kind] == nil {
			c.FolderCategories[kind] = map[string]string{}
		}
	}
	for _, path := range c.MusicFolders {
		if c.FolderCategories["music"][path] == "" {
			c.FolderCategories["music"][path] = c.Categories["music"][0].ID
		}
	}
}
func (a *application) categoryExistsLocked(kind, id string) bool {
	for _, c := range a.config.Categories[kind] {
		if c.ID == id {
			return true
		}
	}
	return false
}
func (a *application) folderCategoryLocked(kind, path string) string {
	for folder, id := range a.config.FolderCategories[kind] {
		if sameLibraryFolder(folder, path) {
			return id
		}
	}
	if kind == "music" {
		return a.config.Categories[kind][0].ID
	}
	return ""
}
func (a *application) mediaCategoryLocked(m media) (string, string) {
	kind := "video"
	if m.Kind == "audio" {
		kind = "music"
	}
	folders := a.config.VideoFolders
	if kind == "music" {
		folders = a.config.MusicFolders
	}
	id := ""
	best := -1
	for _, folder := range folders {
		if sameLibraryFolder(filepath.Dir(m.Path), folder) || (a.config.FolderRecursive[kind][folder] && folderContainsFile(folder, m.Path)) {
			absolute, _ := filepath.Abs(folder)
			if len(absolute) > best {
				best = len(absolute)
				id = a.folderCategoryLocked(kind, folder)
			}
		}
	}
	if best < 0 {
		id = a.folderCategoryLocked(kind, filepath.Dir(m.Path))
	}
	for _, category := range a.config.Categories[kind] {
		if category.ID == id {
			return id, category.Name
		}
	}
	return "", ""
}
func (a *application) categorySnapshotLocked() (map[string][]libraryCategory, map[string]map[string]string) {
	categories := map[string][]libraryCategory{}
	folders := map[string]map[string]string{}
	for _, kind := range []string{"music", "video"} {
		categories[kind] = append([]libraryCategory{}, a.config.Categories[kind]...)
		folders[kind] = map[string]string{}
		for path, id := range a.config.FolderCategories[kind] {
			folders[kind][path] = id
		}
	}
	return categories, folders
}
func (a *application) categoryActionLocked(r actionRequest) error {
	if r.Kind != "music" && r.Kind != "video" {
		return fmt.Errorf("choose music or video categories")
	}
	categories := a.config.Categories[r.Kind]
	if r.Action == "folderCategory" {
		if !a.categoryExistsLocked(r.Kind, r.Category) {
			return fmt.Errorf("choose a category")
		}
		folders := a.config.MusicFolders
		if r.Kind == "video" {
			folders = a.config.VideoFolders
		}
		for _, path := range folders {
			if r.Path != "" && sameLibraryFolder(path, r.Path) {
				a.config.FolderCategories[r.Kind][path] = r.Category
				a.libraryRevision++
				return nil
			}
		}
		return fmt.Errorf("folder is not a configured library source")
	}
	name := strings.TrimSpace(r.Name)
	if r.Action != "deleteCategory" {
		if name == "" || len(name) > 80 || strings.EqualFold(name, "All") {
			return fmt.Errorf("category name must be 1–80 characters and cannot be All")
		}
		for _, category := range categories {
			if category.ID != r.ID && strings.EqualFold(category.Name, name) {
				return fmt.Errorf("category name already exists")
			}
		}
	}
	if r.Action == "addCategory" {
		a.config.Categories[r.Kind] = append(categories, libraryCategory{randomID(), name})
		a.libraryRevision++
		return nil
	}
	for i, category := range categories {
		if category.ID == r.ID {
			if r.Action == "renameCategory" {
				categories[i].Name = name
				a.libraryRevision++
				return nil
			}
			if len(categories) == 1 {
				return fmt.Errorf("keep at least one category for this library")
			}
			folders := a.config.MusicFolders
			if r.Kind == "video" {
				folders = a.config.VideoFolders
			}
			for _, path := range folders {
				if a.folderCategoryLocked(r.Kind, path) == r.ID {
					return fmt.Errorf("reassign the folders using this category before deleting it")
				}
			}
			a.config.Categories[r.Kind] = append(categories[:i], categories[i+1:]...)
			a.libraryRevision++
			return nil
		}
	}
	return fmt.Errorf("category does not exist")
}
