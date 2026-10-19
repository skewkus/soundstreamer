package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(value)
}

func body(w http.ResponseWriter, r *http.Request, destination any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("request must use application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
}

func (a *application) adminCookie() string {
	a.mu.Lock()
	secret, hash := a.config.AuthSecret, a.config.AdminPasswordHash
	a.mu.Unlock()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("soundstreamer-admin-v1"))
	mac.Write([]byte(hash))
	return hex.EncodeToString(mac.Sum(nil))
}

func adminCookieName(r *http.Request) string {
	if r.TLS != nil {
		return "ss_admin_https"
	}
	return "ss_admin"
}

func (a *application) authorized(r *http.Request) bool {
	a.mu.Lock()
	configured := a.config.AdminPasswordHash != ""
	a.mu.Unlock()
	cookie, err := r.Cookie(adminCookieName(r))
	return configured && err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(a.adminCookie())) == 1
}

func (a *application) controlOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "Cross-origin request refused", 403)
			return
		}
		if a.ctx.Err() != nil {
			http.Error(w, "Server shutting down", 503)
			return
		}
		next(w, r)
	}
}

func (a *application) passwordHash() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.AdminPasswordHash
}
func (a *application) loginCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: adminCookieName(r), Value: a.adminCookie(), Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
}

func (a *application) changePassword(current, password string, setup bool) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if len(password) < 8 || len(password) > 256 {
		return fmt.Errorf("password must be 8 to 256 characters")
	}
	oldHash := a.passwordHash()
	if setup && oldHash != "" {
		return fmt.Errorf("admin password has already been set")
	}
	if !setup && !verifyPassword(current, oldHash) {
		return fmt.Errorf("current password is incorrect")
	}
	newHash := hashPassword(password)
	a.mu.Lock()
	oldSecret := a.config.AuthSecret
	a.config.AdminPasswordHash = newHash
	a.config.AuthSecret = randomID() + randomID()
	a.mu.Unlock()
	if err := a.persist(); err != nil {
		a.mu.Lock()
		a.config.AdminPasswordHash = oldHash
		a.config.AuthSecret = oldSecret
		a.mu.Unlock()
		return fmt.Errorf("could not save password: %w", err)
	}
	return nil
}

var errFrozen = errors.New("Controls are frozen by admin")

func (a *application) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "Cross-origin request refused", 403)
			return
		}
		if !a.authorized(r) {
			http.Error(w, "Sign in to admin", 401)
			return
		}
		if a.ctx.Err() != nil {
			http.Error(w, "Server shutting down", 503)
			return
		}
		next(w, r)
	}
}

func (a *application) routes(mux *http.ServeMux) {
	a.desktopRoutes(mux)
	mux.HandleFunc("GET /api/admin/auth", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]bool{"setupRequired": a.passwordHash() == ""})
	})
	mux.HandleFunc("POST /api/admin/setup", a.controlOnly(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Password string `json:"password"`
		}
		if err := body(w, r, &request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := a.changePassword("", request.Password, true); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		a.loginCookie(w, r)
		respond(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/admin/password", a.adminOnly(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Current  string `json:"currentPassword"`
			Password string `json:"newPassword"`
		}
		if err := body(w, r, &request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := a.changePassword(request.Current, request.Password, false); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		a.loginCookie(w, r)
		respond(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/admin/login", func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "Cross-origin request refused", 403)
			return
		}
		var request struct {
			Password string `json:"password"`
		}
		if err := body(w, r, &request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if len(request.Password) > 256 || !verifyPassword(request.Password, a.passwordHash()) {
			http.Error(w, "Incorrect password", 401)
			return
		}
		a.loginCookie(w, r)
		respond(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/admin/logout", a.adminOnly(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: adminCookieName(r), Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		respond(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /api/theme", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		theme := a.config.Theme
		a.mu.Unlock()
		respond(w, map[string]string{"theme": theme})
	})
	mux.HandleFunc("GET /api/admin/state", a.adminOnly(func(w http.ResponseWriter, r *http.Request) { respond(w, a.state()) }))
	mux.HandleFunc("GET /api/control/state", a.controlOnly(func(w http.ResponseWriter, r *http.Request) {
		state := a.state()
		delete(state, "musicFolder")
		delete(state, "videoFolder")
		delete(state, "musicFolders")
		delete(state, "videoFolders")
		delete(state, "folderCategories")
		delete(state, "folderRecursive")
		delete(state, "resumeOnStartup")
		delete(state, "certFile")
		delete(state, "keyFile")
		respond(w, state)
	}))
	mux.HandleFunc("POST /api/control/action", a.controlOnly(func(w http.ResponseWriter, r *http.Request) {
		var request actionRequest
		if err := body(w, r, &request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		allowed := map[string]bool{"play": true, "pause": true, "stop": true, "seek": true, "next": true, "playNow": true, "enqueue": true, "remove": true, "move": true, "clearQueue": true, "shuffle": true, "repeat": true, "volume": true, "savePlaylist": true, "loadPlaylist": true, "deletePlaylist": true, "muteRoom": true}
		if !allowed[request.Action] {
			http.Error(w, "This control is available only in admin", 403)
			return
		}
		if err := a.dispatchAction(request, true); err != nil {
			code := 400
			if errors.Is(err, errFrozen) {
				code = 423
			}
			http.Error(w, err.Error(), code)
			return
		}
		state := a.state()
		delete(state, "musicFolder")
		delete(state, "videoFolder")
		delete(state, "musicFolders")
		delete(state, "videoFolders")
		delete(state, "folderCategories")
		delete(state, "folderRecursive")
		delete(state, "resumeOnStartup")
		delete(state, "certFile")
		delete(state, "keyFile")
		respond(w, state)
	}))
	libraryHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))), r.URL.Query().Get("kind")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}
		if limit <= 0 || limit > 300 {
			limit = 100
		}
		a.mu.Lock()
		results := []media{}
		for _, m := range a.library {
			m = a.displayMediaLocked(m)
			if category := r.URL.Query().Get("category"); category != "" && m.Category != category {
				continue
			}
			if kind != "" && m.Kind != kind {
				continue
			}
			if query == "" || strings.Contains(strings.ToLower(m.Title+" "+m.Artist+" "+m.Album+" "+filepath.Base(m.Path)), query) {
				results = append(results, m)
			}
		}
		a.mu.Unlock()
		sort.Slice(results, func(i, j int) bool {
			artist := func(m media) string {
				if m.Artist == "" && m.Kind == "audio" {
					return "unknown artist"
				}
				return strings.ToLower(m.Artist)
			}
			left, right := artist(results[i]), artist(results[j])
			if left != right {
				return left < right
			}
			left, right = strings.ToLower(results[i].Title), strings.ToLower(results[j].Title)
			if left != right {
				return left < right
			}
			return results[i].ID < results[j].ID
		})
		total := len(results)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		results = results[offset:end]
		respond(w, map[string]any{"items": results, "total": total})
	})
	mux.HandleFunc("GET /api/admin/library", a.adminOnly(libraryHandler))
	mux.HandleFunc("GET /api/control/library", a.controlOnly(libraryHandler))
	mux.HandleFunc("GET /api/admin/folders", a.adminOnly(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			roots := []map[string]string{}
			if runtime.GOOS == "windows" {
				for drive := 'A'; drive <= 'Z'; drive++ {
					p := fmt.Sprintf("%c:\\", drive)
					if info, err := os.Stat(p); err == nil && info.IsDir() {
						roots = append(roots, map[string]string{"name": p, "path": p})
					}
				}
			} else {
				roots = append(roots, map[string]string{"name": "Filesystem", "path": "/"})
				if home, err := os.UserHomeDir(); err == nil {
					roots = append(roots, map[string]string{"name": "Home", "path": home})
				}
			}
			respond(w, map[string]any{"path": "", "parent": "", "folders": roots})
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			http.Error(w, "Invalid folder", 400)
			return
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			http.Error(w, "Cannot read this folder", 400)
			return
		}
		folders := []map[string]string{}
		for _, entry := range entries {
			if entry.IsDir() {
				folders = append(folders, map[string]string{"name": entry.Name(), "path": filepath.Join(abs, entry.Name())})
			}
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			parent = ""
		}
		respond(w, map[string]any{"path": abs, "parent": parent, "folders": folders})
	}))
	mux.HandleFunc("POST /api/admin/action", a.adminOnly(func(w http.ResponseWriter, r *http.Request) {
		var request actionRequest
		if err := body(w, r, &request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := a.action(request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		respond(w, a.state())
	}))
	mux.HandleFunc("POST /api/room", func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "Cross-origin request refused", 403)
			return
		}
		var request struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		}
		if err := body(w, r, &request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if !validRoomID(request.ID) || len(request.Name) > 80 || (request.Kind != "listen" && request.Kind != "watch") {
			http.Error(w, "Invalid room", 400)
			return
		}
		a.mu.Lock()
		room := a.rooms[request.ID]
		if room == nil {
			room = &connectedRoom{ID: request.ID}
			a.rooms[request.ID] = room
		}
		room.Name, room.Kind, room.Seen = strings.TrimSpace(request.Name), request.Kind, time.Now()
		setting := a.config.Rooms[request.ID]
		changed := setting.Name != room.Name
		setting.Name = room.Name
		a.config.Rooms[request.ID] = setting
		volume := a.config.Volume
		a.mu.Unlock()
		if changed {
			a.save()
		}
		respond(w, map[string]any{"muted": setting.Muted, "volume": volume})
	})
}

func validRoomID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return false
		}
	}
	return true
}

func (a *application) roomConnection(id string, delta int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	room := a.rooms[id]
	if room == nil {
		room = &connectedRoom{ID: id, Name: "Unnamed room"}
		a.rooms[id] = room
	}
	room.Connections += delta
	room.Seen = time.Now()
	if room.Connections < 0 {
		room.Connections = 0
	}
}

type actionRequest struct {
	AudioChannels     int     `json:"audioChannels,omitempty"`
	AudioSampleRate   int     `json:"audioSampleRate,omitempty"`
	HTTPSHostname     string  `json:"httpsHostname,omitempty"`
	CertFile          string  `json:"certFile,omitempty"`
	KeyFile           string  `json:"keyFile,omitempty"`
	Category          string  `json:"category,omitempty"`
	IncludeSubfolders bool    `json:"includeSubfolders,omitempty"`
	Action            string  `json:"action"`
	ID                string  `json:"id,omitempty"`
	Key               string  `json:"key,omitempty"`
	To                int     `json:"to,omitempty"`
	Position          float64 `json:"position,omitempty"`
	Value             float64 `json:"value,omitempty"`
	Enabled           bool    `json:"enabled,omitempty"`
	Name              string  `json:"name,omitempty"`
	Repeat            string  `json:"repeat,omitempty"`
	Kind              string  `json:"kind,omitempty"`
	Path              string  `json:"path,omitempty"`
}

func (a *application) action(r actionRequest) error {
	return a.dispatchAction(r, false)
}

func (a *application) dispatchAction(r actionRequest, public bool) error {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.ctx.Err() != nil {
		return fmt.Errorf("server shutting down")
	}
	a.mu.Lock()
	if public && a.config.ControlFrozen {
		a.mu.Unlock()
		return errFrozen
	}
	if public && (r.Action == "pcAudio" || r.Action == "pcScreen" || r.Action == "videoTranscodeSettings" || r.Action == "audioSettings") {
		a.mu.Unlock()
		return fmt.Errorf("This control is available only in admin")
	}
	var start *media
	position := 0.0
	scan := false
	scanFolder := ""
	switch r.Action {
	case "audioSettings":
		if (r.AudioChannels != 1 && r.AudioChannels != 2) || !validAudioSampleRate(r.AudioSampleRate) {
			a.mu.Unlock()
			return fmt.Errorf("choose mono or stereo and a sample rate of 48, 44.1, 32 or 24 kHz")
		}
		a.config.AudioChannels = r.AudioChannels
		a.config.AudioSampleRate = r.AudioSampleRate
	case "serverSettings":
		if r.To < 1 || r.To > 65535 {
			a.mu.Unlock()
			return fmt.Errorf("port must be between 1 and 65535")
		}
		candidate := a.config
		candidate.ListenPort = r.To
		candidate.HTTPSEnabled = r.Enabled
		candidate.HTTPSHostname = strings.TrimSpace(r.HTTPSHostname)
		candidate.CertFile = strings.TrimSpace(r.CertFile)
		candidate.KeyFile = strings.TrimSpace(r.KeyFile)
		// Reading certificate files can take time; do not block playback's mutex.
		a.mu.Unlock()
		if _, err := serverTLSConfig(a.root, candidate); err != nil {
			return err
		}
		a.mu.Lock()
		a.config.ListenPort = candidate.ListenPort
		a.config.HTTPSEnabled = candidate.HTTPSEnabled
		a.config.HTTPSHostname = candidate.HTTPSHostname
		a.config.CertFile = candidate.CertFile
		a.config.KeyFile = candidate.KeyFile
	case "videoTranscodeSettings":
		if !validVideoTranscodeResolution(r.Name) {
			a.mu.Unlock()
			return fmt.Errorf("choose Original, 1080p, 720p or 480p")
		}
		changed := a.config.ForceVideoTranscode != r.Enabled || (r.Enabled && a.config.VideoTranscodeResolution != r.Name)
		a.config.ForceVideoTranscode = r.Enabled
		a.config.VideoTranscodeResolution = r.Name
		if changed && !a.pcAudio && a.config.Current != nil && a.config.Current.Kind == "video" && (a.playing || a.loading) {
			current := *a.config.Current
			start = &current
			position = a.positionLocked(time.Now())
		}
	case "listenPort":
		if r.To < 1 || r.To > 65535 {
			a.mu.Unlock()
			return fmt.Errorf("port must be between 1 and 65535")
		}
		a.config.ListenPort = r.To
	case "pcScreen":
		a.mu.Unlock()
		return a.startPCScreen()
	case "screenAudioOffset":
		if r.To < -3000 || r.To > 3000 {
			a.mu.Unlock()
			return fmt.Errorf("audio offset must be between -3000 and 3000 milliseconds")
		}
		if a.config.ScreenAudioOffsetMs != r.To {
			a.config.ScreenAudioOffsetMs = r.To
			if a.pcScreen != nil {
				if a.playing {
					a.anchorMs = a.screenAnchorBaseMs + float64(screenAudioPadding(a.station.delay, time.Duration(r.To)*time.Millisecond))/float64(time.Millisecond)
				}
				// Flush queued old-offset audio; the capture and PCM sample timeline
				// continue, and all rooms reconnect to the same adjusted session.
				a.session = randomID()
				a.station.resetStreams()
			}
		}
	case "screenFPS":
		if r.To != 30 && r.To != 60 {
			a.mu.Unlock()
			return fmt.Errorf("choose 30 or 60 FPS")
		}
		a.config.ScreenFPS = r.To
	case "pcAudio":
		a.mu.Unlock()
		return a.startPCAudio()
	case "freeze":
		a.config.ControlFrozen = r.Enabled
	case "play":
		if a.pcAudio {
			a.cancelLocked()
		}
		if a.playing || a.loading {
			a.mu.Unlock()
			return nil
		}
		if a.config.Current != nil {
			m := *a.config.Current
			start = &m
			position = a.position
			if position >= m.Duration-0.02 {
				position = 0
			}
		} else {
			start = a.nextLocked(false)
		}
		if start == nil {
			a.mu.Unlock()
			return fmt.Errorf("choose a track or add something to the queue")
		}
	case "pause", "stop":
		if !a.pcAudio {
			a.position = a.positionLocked(time.Now())
		}
		if r.Action == "stop" {
			a.position = 0
		}
		a.cancelLocked()
		a.station.resetStreams()
	case "seek":
		if a.pcAudio {
			a.mu.Unlock()
			return fmt.Errorf("PC audio is live and cannot be seeked")
		}
		if a.config.Current == nil {
			a.mu.Unlock()
			return fmt.Errorf("nothing is selected")
		}
		if math.IsNaN(r.Position) || math.IsInf(r.Position, 0) {
			a.mu.Unlock()
			return fmt.Errorf("invalid seek position")
		}
		position = math.Max(0, math.Min(r.Position, math.Max(0, a.config.Current.Duration-0.02)))
		if a.playing || a.loading {
			m := *a.config.Current
			start = &m
		} else {
			a.position = position
			a.session = randomID()
			a.station.resetStreams()
		}
	case "next":
		start = a.nextLocked(false)
		if start == nil {
			a.position = 0
			a.cancelLocked()
			a.station.resetStreams()
		}
	case "playNow", "enqueue":
		m, ok := a.library[r.ID]
		if !ok {
			a.mu.Unlock()
			return fmt.Errorf("media is no longer in the library; rescan the source folder")
		}
		if r.Action == "playNow" {
			start = &m
		} else {
			a.config.Queue = append(a.config.Queue, queueItem{Key: randomID(), Media: m})
		}
	case "remove", "move":
		index := -1
		for i, item := range a.config.Queue {
			if item.Key == r.Key {
				index = i
				break
			}
		}
		if index < 0 {
			a.mu.Unlock()
			return fmt.Errorf("queue item is no longer present")
		}
		item := a.config.Queue[index]
		if r.Action == "move" && (r.To < 0 || r.To >= len(a.config.Queue)) {
			a.mu.Unlock()
			return fmt.Errorf("invalid queue position")
		}
		a.config.Queue = append(a.config.Queue[:index], a.config.Queue[index+1:]...)
		if r.Action == "move" {
			a.config.Queue = append(a.config.Queue, queueItem{})
			copy(a.config.Queue[r.To+1:], a.config.Queue[r.To:])
			a.config.Queue[r.To] = item
		}
	case "clearQueue":
		a.config.Queue = []queueItem{}
	case "shuffle":
		a.config.Shuffle = r.Enabled
	case "repeat":
		if r.Repeat != "none" && r.Repeat != "one" && r.Repeat != "all" {
			a.mu.Unlock()
			return fmt.Errorf("invalid repeat mode")
		}
		if (a.config.Repeat == "one") != (r.Repeat == "one") && (a.playing || a.loading) && !a.pcAudio && a.config.Current != nil {
			m := *a.config.Current
			start = &m
			position = a.positionLocked(time.Now())
		}
		a.config.Repeat = r.Repeat
	case "volume":
		if r.Value < 0 || r.Value > 1 {
			a.mu.Unlock()
			return fmt.Errorf("volume must be between zero and one")
		}
		a.config.Volume = r.Value
	case "resume":
		a.config.ResumeOnStartup = r.Enabled
	case "savePlaylist":
		name := strings.TrimSpace(r.Name)
		if name == "" || len(name) > 80 {
			a.mu.Unlock()
			return fmt.Errorf("give the playlist a name (up to 80 characters)")
		}
		items := []media{}
		if a.config.Current != nil {
			items = append(items, *a.config.Current)
		}
		for _, item := range a.config.Queue {
			items = append(items, item.Media)
		}
		if len(items) == 0 {
			a.mu.Unlock()
			return fmt.Errorf("select media or add queue items first")
		}
		a.config.Playlists[name] = items
	case "loadPlaylist":
		items, ok := a.config.Playlists[r.Name]
		if !ok {
			a.mu.Unlock()
			return fmt.Errorf("playlist not found")
		}
		if r.Enabled {
			a.config.Queue = []queueItem{}
		}
		for _, m := range items {
			a.config.Queue = append(a.config.Queue, queueItem{Key: randomID(), Media: m})
		}
	case "deletePlaylist":
		delete(a.config.Playlists, r.Name)
	case "muteRoom":
		setting := a.config.Rooms[r.ID]
		setting.Muted = r.Enabled
		a.config.Rooms[r.ID] = setting
	case "folderRecursion":
		if a.scanning {
			a.mu.Unlock()
			return fmt.Errorf("wait for the current library scan to finish")
		}
		var folders []string
		switch r.Kind {
		case "music":
			folders = a.config.MusicFolders
		case "video":
			folders = a.config.VideoFolders
		default:
			a.mu.Unlock()
			return fmt.Errorf("choose music or video")
		}
		for _, path := range folders {
			if r.Path != "" && sameLibraryFolder(path, r.Path) {
				if a.config.FolderRecursive[r.Kind][path] != r.Enabled {
					a.config.FolderRecursive[r.Kind][path] = r.Enabled
					scan = true
					scanFolder = path
					a.libraryRevision++
				}
				break
			}
		}
		if scanFolder == "" && !containsConfiguredFolder(folders, r.Path) {
			a.mu.Unlock()
			return fmt.Errorf("folder is not a configured library source")
		}
	case "rescanFolder":
		if a.scanning {
			a.mu.Unlock()
			return fmt.Errorf("a library scan is already running")
		}
		var folders []string
		switch r.Kind {
		case "music":
			folders = a.config.MusicFolders
		case "video":
			folders = a.config.VideoFolders
		default:
			a.mu.Unlock()
			return fmt.Errorf("choose a music or video source")
		}
		found := false
		for _, path := range folders {
			if r.Path != "" && sameLibraryFolder(path, r.Path) {
				scanFolder = path
				found = true
				break
			}
		}
		if !found {
			a.mu.Unlock()
			return fmt.Errorf("folder is not a configured library source")
		}
		scan = true
	case "rescan":
		if a.scanning {
			a.mu.Unlock()
			return fmt.Errorf("a library scan is already running")
		}
		scan = true
	case "titleSource":
		if r.Name != "filename" && r.Name != "metadata" {
			a.mu.Unlock()
			return fmt.Errorf("choose filename or metadata titles")
		}
		if a.config.TitleSource != r.Name {
			a.config.TitleSource = r.Name
			a.libraryRevision++
		}
	case "addCategory", "renameCategory", "deleteCategory", "folderCategory":
		if err := a.categoryActionLocked(r); err != nil {
			a.mu.Unlock()
			return err
		}
	case "theme":
		if !validTheme(r.Name) {
			a.mu.Unlock()
			return fmt.Errorf("unknown theme")
		}
		a.config.Theme = r.Name
	case "source", "addSource", "removeSource":
		if a.scanning {
			a.mu.Unlock()
			return fmt.Errorf("wait for the current library scan to finish")
		}
		if r.Kind != "music" && r.Kind != "video" {
			a.mu.Unlock()
			return fmt.Errorf("choose a music or video source")
		}
		path := strings.TrimSpace(r.Path)
		if r.Action != "source" && path == "" {
			a.mu.Unlock()
			return fmt.Errorf("choose a folder")
		}
		if path != "" {
			abs, err := filepath.Abs(path)
			if err != nil {
				a.mu.Unlock()
				return fmt.Errorf("invalid folder")
			}
			info, err := os.Stat(abs)
			if r.Action != "removeSource" && (err != nil || !info.IsDir()) {
				a.mu.Unlock()
				return fmt.Errorf("source folder is missing or unreadable")
			}
			path = abs
		}
		folders := &a.config.MusicFolders
		if r.Kind == "video" {
			folders = &a.config.VideoFolders
		}
		category := r.Category
		if r.Action != "removeSource" && path != "" {
			if category == "" && r.Kind == "music" {
				category = a.config.Categories["music"][0].ID
			}
			if !a.categoryExistsLocked(r.Kind, category) {
				a.mu.Unlock()
				return fmt.Errorf("choose a category before adding a folder")
			}
		}
		switch r.Action {
		case "source":
			a.config.FolderCategories[r.Kind] = map[string]string{}
			a.config.FolderRecursive[r.Kind] = map[string]bool{}
			*folders = []string{}
			if path != "" {
				*folders = append(*folders, path)
			}
		case "addSource":
			if path == "" {
				a.mu.Unlock()
				return fmt.Errorf("choose a folder")
			}
			found := false
			for _, existing := range *folders {
				if existing == path {
					found = true
				}
			}
			if !found {
				*folders = append(*folders, path)
			}
		case "removeSource":
			kept := []string{}
			for _, existing := range *folders {
				if existing != path {
					kept = append(kept, existing)
				}
			}
			*folders = kept
		}
		if r.Action == "removeSource" {
			delete(a.config.FolderCategories[r.Kind], path)
			delete(a.config.FolderRecursive[r.Kind], path)
		} else if path != "" {
			a.config.FolderCategories[r.Kind][path] = category
			a.config.FolderRecursive[r.Kind][path] = r.IncludeSubfolders
		}
		a.libraryRevision++
		// Adding a source only probes that source. Removing one prunes its
		// cached entries while preserving files still owned by other sources.
		if r.Action == "addSource" || r.Action == "removeSource" {
			scanFolder = path
		}
		scan = true
	case "clearError":
		a.lastError = ""
	default:
		a.mu.Unlock()
		return fmt.Errorf("unknown control")
	}
	a.mu.Unlock()
	if start != nil {
		a.play(*start, position)
	} else {
		a.save()
	}
	if scan {
		a.scanLibraryFolder(scanFolder)
	}
	return nil
}
