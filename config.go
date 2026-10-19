package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type queueItem struct {
	Key   string `json:"key"`
	Media media  `json:"media"`
}
type roomSetting struct {
	Name  string `json:"name"`
	Muted bool   `json:"muted"`
}
type settings struct {
	AudioChannels            int                          `json:"audioChannels"`
	AudioSampleRate          int                          `json:"audioSampleRate"`
	ForceVideoTranscode      bool                         `json:"forceVideoTranscode"`
	VideoTranscodeResolution string                       `json:"videoTranscodeResolution"`
	HTTPSHostname            string                       `json:"httpsHostname"`
	HTTPSEnabled             bool                         `json:"httpsEnabled"`
	CertFile                 string                       `json:"certFile"`
	KeyFile                  string                       `json:"keyFile"`
	ListenPort               int                          `json:"port"`
	FolderRecursive          map[string]map[string]bool   `json:"folderRecursive"`
	Categories               map[string][]libraryCategory `json:"categories"`
	FolderCategories         map[string]map[string]string `json:"folderCategories"`
	TitleSource              string                       `json:"titleSource"`
	Version                  int                          `json:"version"`
	AdminPasswordHash        string                       `json:"adminPasswordHash"`
	AuthSecret               string                       `json:"authSecret"`
	ControlFrozen            bool                         `json:"controlFrozen"`
	MusicFolder              string                       `json:"musicFolder,omitempty"`
	MusicFolders             []string                     `json:"musicFolders"`
	VideoFolders             []string                     `json:"videoFolders"`
	ScreenAudioOffsetMs      int                          `json:"screenAudioOffsetMs"`
	ScreenFPS                int                          `json:"screenFPS"`
	Theme                    string                       `json:"theme"`
	VideoFolder              string                       `json:"videoFolder,omitempty"`
	Current                  *media                       `json:"current,omitempty"`
	Position                 float64                      `json:"position"`
	Queue                    []queueItem                  `json:"queue"`
	Playlists                map[string][]media           `json:"playlists"`
	Rooms                    map[string]roomSetting       `json:"rooms"`
	Repeat                   string                       `json:"repeat"`
	Shuffle                  bool                         `json:"shuffle"`
	Volume                   float64                      `json:"volume"`
	ResumeOnStartup          bool                         `json:"resumeOnStartup"`
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func loadSettings(root string) (settings, error) {
	c := settings{Version: 1, Volume: 1, Repeat: "none", Queue: []queueItem{}, Playlists: map[string][]media{}, Rooms: map[string]roomSetting{}}
	b, err := os.ReadFile(filepath.Join(root, "soundstreamer.json"))
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("soundstreamer.json is invalid: %w", err)
		}
		var legacy struct {
			Password string `json:"adminPassword"`
		}
		if err := json.Unmarshal(b, &legacy); err != nil {
			return c, err
		}
		if c.AdminPasswordHash == "" && legacy.Password != "" {
			c.AdminPasswordHash = hashPassword(legacy.Password)
		}
	} else if !os.IsNotExist(err) {
		return c, err
	}
	if c.Version != 1 {
		return c, fmt.Errorf("unsupported configuration version %d", c.Version)
	}
	if c.AuthSecret == "" {
		c.AuthSecret = randomID() + randomID()
	}
	if c.Playlists == nil {
		c.Playlists = map[string][]media{}
	}
	if c.Rooms == nil {
		c.Rooms = map[string]roomSetting{}
	}
	if c.Queue == nil {
		c.Queue = []queueItem{}
	}
	if c.Volume < 0 || c.Volume > 1 {
		c.Volume = 1
	}
	if c.Repeat != "none" && c.Repeat != "one" && c.Repeat != "all" {
		c.Repeat = "none"
	}
	if len(c.MusicFolders) == 0 && c.MusicFolder != "" {
		c.MusicFolders = []string{c.MusicFolder}
	}
	if len(c.VideoFolders) == 0 && c.VideoFolder != "" {
		c.VideoFolders = []string{c.VideoFolder}
	}
	c.MusicFolder, c.VideoFolder = "", ""
	if c.MusicFolders == nil {
		c.MusicFolders = []string{}
	}
	if c.VideoFolders == nil {
		c.VideoFolders = []string{}
	}
	if c.TitleSource != "filename" && c.TitleSource != "metadata" {
		c.TitleSource = "filename"
	}
	if c.ScreenAudioOffsetMs < -3000 || c.ScreenAudioOffsetMs > 3000 {
		c.ScreenAudioOffsetMs = 0
	}
	if c.ScreenFPS != 30 && c.ScreenFPS != 60 {
		c.ScreenFPS = 30
	}
	if !validTheme(c.Theme) {
		c.Theme = "green"
	}
	if c.ListenPort < 1 || c.ListenPort > 65535 {
		c.ListenPort = 8080
	}
	if !validVideoTranscodeResolution(c.VideoTranscodeResolution) {
		c.VideoTranscodeResolution = "720p"
	}
	if c.AudioChannels != 1 && c.AudioChannels != 2 {
		c.AudioChannels = 2
	}
	if !validAudioSampleRate(c.AudioSampleRate) {
		c.AudioSampleRate = 48000
	}
	c.initCategories()
	return c, nil
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeJSONBytes(path, b)
}

func writeJSONBytes(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".soundstreamer-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func validTheme(theme string) bool {
	switch theme {
	case "green", "blue", "red", "purple", "amber", "teal":
		return true
	}
	return false
}
