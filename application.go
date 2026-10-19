package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type connectedRoom struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Muted       bool      `json:"muted"`
	Connections int       `json:"connections"`
	Seen        time.Time `json:"-"`
}

type application struct {
	audio              audioFormat // Immutable until the server restarts.
	activeHTTPS        bool
	activePort         int
	libraryCacheLoaded bool
	libraryRevision    uint64
	mu                 sync.Mutex
	controlMu          sync.Mutex
	persistMu          sync.Mutex
	ctx                context.Context
	root               string
	station            *station
	videoChunks        *videoChunks
	config             settings
	library            map[string]media
	rooms              map[string]*connectedRoom
	screenAnchorBaseMs float64
	pcScreen           *desktopStream
	desktopInput       func(int) ([]string, error)
	pcAudio            bool
	pcAudioLastPacket  time.Time
	pcAudioSource      func(context.Context) (*exec.Cmd, error)
	playing            bool
	looping            bool
	loading            bool
	progress           float64
	position           float64
	anchorMs           float64
	session            string
	video              *videoSource
	generation         uint64
	cancelPlayback     context.CancelFunc
	scanCancel         context.CancelFunc
	scanning           bool
	scanFolder         string
	scanDone           int
	scanTotal          int
	scanErrors         int
	lastError          string
	workers            sync.WaitGroup
}

func newApplication(ctx context.Context, root string, delay time.Duration) (*application, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	c, err := loadSettings(root)
	if err != nil {
		return nil, err
	}
	a := &application{audio: audioFormat{SampleRate: c.AudioSampleRate, Channels: c.AudioChannels}, ctx: ctx, root: root, config: c, library: map[string]media{}, rooms: map[string]*connectedRoom{}, session: randomID(), position: c.Position, pcAudioSource: pcAudioCommand, desktopInput: desktopInputArgs}
	a.station = newStation(delay, "Nothing playing")
	a.videoChunks = newVideoChunks(ctx)
	a.station.app = a
	var cached []media
	if b, err := os.ReadFile(filepath.Join(root, "soundstreamer-library.json")); err == nil {
		if err := json.Unmarshal(b, &cached); err == nil {
			a.libraryCacheLoaded = true
			for _, m := range cached {
				a.library[m.ID] = m
			}
		}
	}
	if err := a.persist(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *application) positionLocked(now time.Time) float64 {
	position := a.position
	if a.playing {
		position += math.Max(0, float64(now.Sub(a.station.origin))/float64(time.Millisecond)-a.anchorMs) / 1000
	}
	if a.pcAudio {
		return 0
	}
	if a.config.Current != nil {
		if a.looping && a.config.Current.Duration > 0 {
			position = math.Mod(position, a.config.Current.Duration)
		}
		position = math.Min(position, a.config.Current.Duration)
	}
	return math.Max(0, position)
}

func (a *application) persist() error {
	a.persistMu.Lock()
	defer a.persistMu.Unlock()
	a.mu.Lock()
	if !a.pcAudio {
		a.config.Position = a.positionLocked(time.Now())
	}
	// Marshal while locked so maps/slices cannot change during a save.
	b, err := json.MarshalIndent(a.config, "", "  ")
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return writeJSONBytes(filepath.Join(a.root, "soundstreamer.json"), b)
}

func (a *application) save() {
	if err := a.persist(); err != nil {
		a.mu.Lock()
		a.lastError = "Could not save configuration: " + err.Error()
		a.mu.Unlock()
		log.Print(err)
	}
}

func (a *application) clock() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	mode, title, artist := "idle", "Nothing playing", ""
	if a.config.Current != nil {
		title = a.displayMediaLocked(*a.config.Current).Title
		artist = a.config.Current.Artist
		mode = "file"
		if a.config.Current.Kind == "video" {
			mode = "video"
		}
	}
	if a.pcAudio {
		mode, title, artist = "pc", "PC audio", ""
	}
	if a.pcScreen != nil {
		mode, title, artist = "video", "PC screen + audio", ""
	}
	state := map[string]any{"serverMs": float64(time.Since(a.station.origin)) / float64(time.Millisecond), "sampleRate": a.audio.SampleRate, "channels": a.audio.Channels, "bufferMs": a.station.delay.Milliseconds(), "title": title, "artist": artist, "mode": mode, "session": a.session, "volume": a.config.Volume, "listeners": a.roomCountLocked(), "playback": map[string]any{"playing": a.playing, "loading": a.loading, "position": a.playbackPositionLocked(), "anchorMs": a.anchorMs, "loop": a.looping}}
	if a.pcScreen != nil {
		state["video"] = map[string]any{"url": "/api/desktop", "live": true, "transcode": true, "chunkSeconds": desktopChunkSeconds, "audioOffsetMs": a.config.ScreenAudioOffsetMs}
	} else if a.video != nil {
		state["video"] = map[string]any{"url": "/api/video", "duration": a.video.Duration, "pictureDuration": a.video.pictureDuration(), "transcode": a.video.Transcode, "chunkSeconds": videoChunkSeconds}
	}
	return state
}

func (a *application) roomCountLocked() int {
	n := 0
	for _, r := range a.rooms {
		if r.Connections > 0 {
			n++
		}
	}
	return n
}

func (a *application) state() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	rooms := []connectedRoom{}
	for id, r := range a.rooms {
		if r.Connections > 0 {
			room := *r
			setting := a.config.Rooms[id]
			room.Muted = setting.Muted
			if setting.Name != "" {
				room.Name = setting.Name
			}
			rooms = append(rooms, room)
		}
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Name < rooms[j].Name })
	playlists := []string{}
	for name := range a.config.Playlists {
		playlists = append(playlists, name)
	}
	sort.Strings(playlists)
	queue := append([]queueItem{}, a.config.Queue...)
	for i := range queue {
		queue[i].Media = a.displayMediaLocked(queue[i].Media)
	}
	var current *media
	if a.config.Current != nil {
		m := a.displayMediaLocked(*a.config.Current)
		current = &m
	}
	categories, folderCategories := a.categorySnapshotLocked()
	return map[string]any{"audioChannels": a.config.AudioChannels, "audioSampleRate": a.config.AudioSampleRate, "activeAudioChannels": a.audio.Channels, "activeAudioSampleRate": a.audio.SampleRate, "forceVideoTranscode": a.config.ForceVideoTranscode, "videoTranscodeResolution": a.config.VideoTranscodeResolution, "httpsHostname": a.config.HTTPSHostname, "httpsEnabled": a.config.HTTPSEnabled, "certFile": a.config.CertFile, "keyFile": a.config.KeyFile, "activeHTTPS": a.activeHTTPS, "version": appVersion, "port": a.config.ListenPort, "activePort": a.activePort, "categories": categories, "folderCategories": folderCategories, "folderRecursive": a.recursiveSnapshotLocked(), "pcScreen": a.pcScreen != nil, "screenAudioOffsetMs": a.config.ScreenAudioOffsetMs, "screenFPS": a.config.ScreenFPS, "pcAudio": a.pcAudio, "pcAudioAvailable": runtime.GOOS == "windows", "frozen": a.config.ControlFrozen, "titleSource": a.config.TitleSource, "current": current, "position": a.positionLocked(time.Now()), "playing": a.playing, "loading": a.loading, "progress": a.progress, "queue": queue, "rooms": rooms, "playlists": playlists, "repeat": a.config.Repeat, "shuffle": a.config.Shuffle, "volume": a.config.Volume, "resumeOnStartup": a.config.ResumeOnStartup, "musicFolders": append([]string{}, a.config.MusicFolders...), "videoFolders": append([]string{}, a.config.VideoFolders...), "theme": a.config.Theme, "libraryRevision": a.libraryRevision, "libraryCount": len(a.library), "scanning": a.scanning, "scanFolder": a.scanFolder, "scanDone": a.scanDone, "scanTotal": a.scanTotal, "scanErrors": a.scanErrors, "error": a.lastError}
}

func (a *application) displayMediaLocked(m media) media {
	m.Category, m.CategoryName = a.mediaCategoryLocked(m)
	if a.config.TitleSource == "filename" && m.Path != "" {
		m.Title = strings.TrimSuffix(filepath.Base(m.Path), filepath.Ext(m.Path))
	}
	return m
}

func (a *application) playbackPositionLocked() float64 {
	if a.pcScreen != nil {
		return 0
	}
	return a.position
}

func (a *application) cancelLocked() {
	if a.pcScreen != nil {
		a.pcScreen.close()
		a.pcScreen = nil
		a.video = nil
	}
	if a.cancelPlayback != nil {
		a.cancelPlayback()
		a.cancelPlayback = nil
	}
	a.generation++
	a.playing, a.loading = false, false
	a.pcAudio = false
	a.looping = false
	a.session = randomID()
}

func (a *application) play(m media, position float64) {
	format := a.audio
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	a.cancelLocked()
	a.config.Current = &m
	a.position = math.Max(0, math.Min(position, math.Max(0, m.Duration-0.02)))
	a.video = nil
	a.loading = true
	a.progress = 0
	a.lastError = ""
	generation := a.generation
	initialPosition := a.position
	loop := a.config.Repeat == "one"
	forceTranscode, transcodeResolution := a.config.ForceVideoTranscode, a.config.VideoTranscodeResolution
	ctx, cancel := context.WithCancel(a.ctx)
	a.cancelPlayback = cancel
	a.workers.Add(1)
	a.station.resetStreams()
	a.mu.Unlock()
	a.save()
	go func() {
		defer a.workers.Done()
		actual, p, err := probeMedia(ctx, m.Path)
		var video *videoSource
		if err == nil && actual.Kind == "video" {
			video, err = prepareVideo(ctx, a.root, actual, p, func(progress float64) {
				a.mu.Lock()
				if a.generation == generation {
					a.progress = progress
				}
				a.mu.Unlock()
			})
			if err == nil {
				video = forcedVideoSource(video, forceTranscode, transcodeResolution)
				actual.Duration = video.Duration
				if video.Transcode {
					a.mu.Lock()
					if a.generation == generation {
						a.progress = 0
					}
					a.mu.Unlock()
					index := int(math.Min(initialPosition, math.Max(0, video.pictureDuration()-0.04)) / videoChunkSeconds)
					_, err = a.videoChunks.get(ctx, video, index)
				}
			}
		}
		if err != nil {
			a.playbackError(generation, ctx, err)
			return
		}
		path := actual.Path
		if video != nil {
			path = video.Path
		}
		a.mu.Lock()
		if a.generation != generation || ctx.Err() != nil {
			a.mu.Unlock()
			return
		}
		a.config.Current = &actual
		a.video = video
		position := math.Min(a.position, math.Max(0, actual.Duration-0.02))
		a.position = position
		a.mu.Unlock()
		args := decoderArgs(path, position, actual.Duration, loop, format)
		if video != nil && !video.HasAudio {
			args = silentDecoderArgs(actual.Duration-position, loop, format)
		}
		cmd := backgroundCommand(ctx, "ffmpeg", args...)
		cmd.Stderr = os.Stderr
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			a.playbackError(generation, ctx, err)
			return
		}
		if err = cmd.Start(); err != nil {
			a.playbackError(generation, ctx, fmt.Errorf("start audio decoder: %w", err))
			return
		}
		// Read the first chunk before anchoring the playback clock.
		first := make([]byte, format.chunkBytes())
		n, readErr := io.ReadFull(pipe, first)
		if n == 0 {
			cancel()
			cmd.Wait()
			a.playbackError(generation, ctx, fmt.Errorf("media produced no audio"))
			return
		}
		a.mu.Lock()
		if a.generation != generation || ctx.Err() != nil {
			a.mu.Unlock()
			cancel()
			cmd.Wait()
			return
		}
		start := time.Now()
		a.anchorMs = float64(start.Add(a.station.delay).Sub(a.station.origin)) / float64(time.Millisecond)
		a.loading = false
		a.playing = true
		a.looping = loop
		a.progress = 100
		a.station.resetStreams()
		a.mu.Unlock()
		a.save()
		frame := int64(0)
		pcm := first
		for {
			at := start.Add(time.Duration(frame) * time.Second / time.Duration(format.SampleRate))
			timer := time.NewTimer(time.Until(at))
			select {
			case <-ctx.Done():
				timer.Stop()
				cmd.Wait()
				return
			case <-timer.C:
			}
			a.mu.Lock()
			if a.generation != generation || ctx.Err() != nil {
				a.mu.Unlock()
				cmd.Wait()
				return
			}
			a.station.publish(format.packet(at.Add(a.station.delay).Sub(a.station.origin), pcm))
			a.mu.Unlock()
			frame += int64(format.chunkFrames())
			if readErr != nil {
				break
			}
			pcm = make([]byte, format.chunkBytes())
			_, readErr = io.ReadFull(pipe, pcm)
			if readErr == io.EOF {
				break
			}
			if readErr != nil && readErr != io.ErrUnexpectedEOF {
				break
			}
		}
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if waitErr != nil {
			a.playbackError(generation, ctx, fmt.Errorf("audio decoding failed: %w", waitErr))
			return
		}
		// Let the buffered end of the track reach speakers before changing session.
		timer := time.NewTimer(time.Until(start.Add(time.Duration(frame) * time.Second / time.Duration(format.SampleRate)).Add(a.station.delay)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		a.ended(generation)
	}()
}

func decoderArgs(path string, position, duration float64, loop bool, format audioFormat) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	seek := strconv.FormatFloat(position, 'f', 6, 64)
	if loop {
		// Input seeking would repeat only the tail of the file on every loop.
		args = append(args, "-stream_loop", "-1", "-i", path, "-ss", seek)
	} else {
		args = append(args, "-ss", seek, "-i", path)
	}
	args = append(args, "-map", "0:a:0", "-vn", "-af", "aresample="+strconv.Itoa(format.SampleRate)+":async=1:first_pts=0,apad")
	if !loop {
		args = append(args, "-t", strconv.FormatFloat(duration-position, 'f', 6, 64))
	}
	return append(args, "-f", "s16le", "-acodec", "pcm_s16le", "-ar", strconv.Itoa(format.SampleRate), "-ac", strconv.Itoa(format.Channels), "pipe:1")
}

func silentDecoderArgs(duration float64, loop bool, format audioFormat) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc=r=" + strconv.Itoa(format.SampleRate) + ":cl=" + format.channelLayout()}
	if !loop {
		args = append(args, "-t", strconv.FormatFloat(duration, 'f', 6, 64))
	}
	return append(args, "-f", "s16le", "-acodec", "pcm_s16le", "-ar", strconv.Itoa(format.SampleRate), "-ac", strconv.Itoa(format.Channels), "pipe:1")
}

func (a *application) playbackError(generation uint64, ctx context.Context, err error) {
	a.mu.Lock()
	if a.generation != generation || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	if !a.pcAudio {
		a.position = a.positionLocked(time.Now())
	}
	a.playing, a.loading, a.looping = false, false, false
	a.lastError = err.Error()
	a.session = randomID()
	a.station.resetStreams()
	a.mu.Unlock()
	a.save()
}

func (a *application) nextLocked(ended bool) *media {
	if ended && a.config.Repeat == "one" && a.config.Current != nil {
		m := *a.config.Current
		return &m
	}
	if a.config.Repeat == "all" && a.config.Current != nil {
		a.config.Queue = append(a.config.Queue, queueItem{Key: randomID(), Media: *a.config.Current})
	}
	if len(a.config.Queue) == 0 {
		return nil
	}
	index := 0
	if a.config.Shuffle {
		index = rand.Intn(len(a.config.Queue))
	}
	m := a.config.Queue[index].Media
	a.config.Queue = append(a.config.Queue[:index], a.config.Queue[index+1:]...)
	return &m
}

func (a *application) ended(generation uint64) {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.Lock()
	if a.generation != generation {
		a.mu.Unlock()
		return
	}
	if next := a.nextLocked(true); next != nil {
		a.mu.Unlock()
		a.play(*next, 0)
		return
	}
	a.position = a.config.Current.Duration
	a.cancelLocked()
	a.station.resetStreams()
	a.mu.Unlock()
	a.save()
}

func (a *application) background() {
	format := a.audio
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		saveTicker := time.NewTicker(5 * time.Second)
		defer saveTicker.Stop()
		idleTicker := time.NewTicker(20 * time.Millisecond)
		defer idleTicker.Stop()
		zeros := make([]byte, format.chunkBytes())
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-saveTicker.C:
				a.save()
			case at := <-idleTicker.C:
				a.mu.Lock()
				if !a.playing {
					a.station.publish(format.packet(at.Add(a.station.delay).Sub(a.station.origin), zeros))
				}
				a.mu.Unlock()
			}
		}
	}()
	a.mu.Lock()
	resume, current, position := a.config.ResumeOnStartup, a.config.Current, a.position
	scanNeeded := !a.libraryCacheLoaded && (len(a.config.MusicFolders) > 0 || len(a.config.VideoFolders) > 0)
	a.mu.Unlock()
	if scanNeeded {
		a.scanLibrary()
	}
	if resume && current != nil {
		a.play(*current, position)
	}
}

func (a *application) shutdown() {
	a.mu.Lock()
	if !a.pcAudio {
		a.position = a.positionLocked(time.Now())
	}
	a.cancelLocked()
	if a.scanCancel != nil {
		a.scanCancel()
	}
	a.station.resetStreams()
	a.mu.Unlock()
	a.workers.Wait()
	a.videoChunks.close()
	a.save()
}

var musicExtensions = map[string]bool{".mp3": true, ".flac": true, ".wav": true, ".ogg": true, ".opus": true, ".m4a": true, ".aac": true, ".wma": true, ".aiff": true}
var videoExtensions = map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".webm": true, ".wmv": true, ".m4v": true, ".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true, ".ogv": true}

func sameLibraryFolder(first, second string) bool {
	firstAbs, firstErr := filepath.Abs(first)
	secondAbs, secondErr := filepath.Abs(second)
	if firstErr != nil || secondErr != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(firstAbs, secondAbs)
	}
	return firstAbs == secondAbs
}
func (a *application) scanLibrary() { a.scanLibraryFolder("") }
func (a *application) scanLibraryFolder(target string) {
	a.mu.Lock()
	if a.scanning || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.scanCancel = cancel
	a.scanning = true
	a.scanFolder = target
	a.scanDone, a.scanTotal, a.scanErrors = 0, 0, 0
	sources := a.librarySourcesLocked()
	a.workers.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.workers.Done()
		defer cancel()
		paths := map[string]bool{}
		canReplace := true
		for _, folder := range sources {
			if folder.path == "" || (target != "" && !sameLibraryFolder(folder.path, target)) {
				continue
			}
			err := filepath.WalkDir(folder.path, func(path string, d os.DirEntry, err error) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					if target != "" {
						canReplace = false
					}
					a.mu.Lock()
					if target != "" {
						a.lastError = "Library scan: " + err.Error()
					}
					a.scanErrors++
					a.mu.Unlock()
					return nil
				}
				if d.IsDir() && path != folder.path && !folder.recursive {
					return filepath.SkipDir
				}
				if !d.IsDir() && d.Type()&os.ModeSymlink == 0 && folder.extensions[strings.ToLower(filepath.Ext(path))] {
					paths[path] = true
				}
				return nil
			})
			if err != nil && ctx.Err() == nil {
				a.mu.Lock()
				a.lastError = "Library scan: " + err.Error()
				a.mu.Unlock()
			}
		}
		a.mu.Lock()
		a.scanTotal = len(paths)
		a.mu.Unlock()
		library := map[string]media{}
		var lock sync.Mutex
		jobs := make(chan string)
		var workers sync.WaitGroup
		for i := 0; i < 4; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for path := range jobs {
					m, _, err := probeMedia(ctx, path)
					if err == nil {
						lock.Lock()
						library[m.ID] = m
						lock.Unlock()
					}
					a.mu.Lock()
					a.scanDone++
					if err != nil {
						a.scanErrors++
					}
					a.mu.Unlock()
				}
			}()
		}
		for path := range paths {
			select {
			case <-ctx.Done():
			case jobs <- path:
			}
			if ctx.Err() != nil {
				break
			}
		}
		close(jobs)
		workers.Wait()
		a.mu.Lock()
		if ctx.Err() == nil {
			if target == "" {
				a.libraryRevision++
				a.library = library
			} else if canReplace {
				a.libraryRevision++
				for id, m := range a.library {
					if folderContainsFile(target, m.Path) {
						preserve := false
						covered := false
						for _, source := range sources {
							if sameLibraryFolder(source.path, target) && source.owns(m.Path) {
								covered = true
								break
							}
						}
						if !covered && !paths[m.Path] {
							for _, source := range sources {
								if !sameLibraryFolder(source.path, target) && source.owns(m.Path) {
									preserve = true
									break
								}
							}
						}
						if !preserve {
							delete(a.library, id)
						}
					}
				}
				for id, m := range library {
					a.library[id] = m
				}
			}
		}
		cached := make([]media, 0, len(a.library))
		for _, m := range a.library {
			cached = append(cached, m)
		}
		a.mu.Unlock()
		if ctx.Err() == nil {
			if err := writeJSON(filepath.Join(a.root, "soundstreamer-library.json"), cached); err != nil {
				log.Print(err)
			}
		}
		a.mu.Lock()
		a.scanning = false
		a.scanCancel = nil
		a.scanFolder = ""
		a.mu.Unlock()
	}()
}
