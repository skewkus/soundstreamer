package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sampleRate = 48000
const chunkSamples = 960 // Default-rate frames in a 20 ms packet.

//go:embed web/* branding/sound-streamer.png LICENSE THIRD_PARTY_NOTICES.md
var assets embed.FS

type station struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
	origin  time.Time
	delay   time.Duration
	title   string
	mode    string
	video   *videoSource
	app     *application
}

func newStation(delay time.Duration, title string) *station {
	return &station{clients: make(map[chan []byte]struct{}), origin: time.Now(), delay: delay, title: title, mode: "pulse"}
}

func (s *station) publish(packet []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- packet:
		default:
			// A stalled room must reconnect rather than replay a growing backlog.
			delete(s.clients, ch)
			close(ch)
		}
	}
}

func (s *station) resetStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		close(ch)
		delete(s.clients, ch)
	}
}

func (s *station) handler() http.Handler {
	mux := http.NewServeMux()
	if s.app != nil {
		s.app.routes(mux)
	}
	mux.HandleFunc("GET /api/clock", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if s.app != nil {
			json.NewEncoder(w).Encode(s.app.clock())
			return
		}
		s.mu.Lock()
		listeners := len(s.clients)
		s.mu.Unlock()
		state := map[string]any{"serverMs": float64(time.Since(s.origin)) / float64(time.Millisecond), "sampleRate": sampleRate, "channels": 1, "bufferMs": s.delay.Milliseconds(), "title": s.title, "listeners": listeners, "mode": s.mode, "session": fmt.Sprintf("%x", s.origin.UnixNano())}
		if s.video != nil {
			state["video"] = map[string]any{"url": "/api/video", "duration": s.video.Duration}
		}
		json.NewEncoder(w).Encode(state)
	})
	mux.HandleFunc("GET /api/audio", s.stream)
	mux.HandleFunc("GET /api/video", func(w http.ResponseWriter, r *http.Request) {
		video := s.video
		if s.app != nil {
			s.app.mu.Lock()
			video = s.app.video
			session := s.app.session
			s.app.mu.Unlock()
			if requested := r.URL.Query().Get("session"); requested != "" && requested != session {
				http.Error(w, "Video session changed", http.StatusConflict)
				return
			}
		}
		if video == nil {
			http.NotFound(w, r)
			return
		}
		if video.Transcode && s.app != nil {
			index, err := strconv.Atoi(r.URL.Query().Get("segment"))
			if err != nil || index < 0 || float64(index)*videoChunkSeconds >= video.pictureDuration() {
				http.Error(w, "Invalid video chunk", 400)
				return
			}
			chunk, err := s.app.videoChunks.get(r.Context(), video, index)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("X-Video-Codec", chunk.Codec)
			w.Header().Set("Cache-Control", "no-store")
			http.ServeContent(w, r, "chunk.mp4", time.Time{}, bytes.NewReader(chunk.Data))
			return
		}
		// ServeContent provides byte-range requests needed for late joins and seeks.
		file, err := os.Open(video.Path)
		if err != nil {
			http.Error(w, "Video unavailable", http.StatusServiceUnavailable)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			http.Error(w, "Video unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		name, contentType := "", ""
		switch r.URL.Path {
		case "/", "/listen", "/watch":
			http.Redirect(w, r, "/stream", http.StatusTemporaryRedirect)
			return
		case "/stream":
			name, contentType = "web/stream.html", "text/html; charset=utf-8"
		case "/admin", "/control":
			name, contentType = "web/admin.html", "text/html; charset=utf-8"
		case "/admin.js":
			name, contentType = "web/admin.js", "text/javascript; charset=utf-8"
		case "/admin.css":
			name, contentType = "web/admin.css", "text/css; charset=utf-8"
		case "/app.js":
			name, contentType = "web/app.js", "text/javascript; charset=utf-8"
		case "/video.js":
			name, contentType = "web/video.js", "text/javascript; charset=utf-8"
		case "/desktop.js":
			name, contentType = "web/desktop.js", "text/javascript; charset=utf-8"
		case "/transcode.js":
			name, contentType = "web/transcode.js", "text/javascript; charset=utf-8"
		case "/video.css":
			name, contentType = "web/video.css", "text/css; charset=utf-8"
		case "/theme.js":
			name, contentType = "web/theme.js", "text/javascript; charset=utf-8"
		case "/license":
			name, contentType = "LICENSE", "text/plain; charset=utf-8"
		case "/third-party-notices":
			name, contentType = "THIRD_PARTY_NOTICES.md", "text/plain; charset=utf-8"
		case "/icon.png":
			name, contentType = "branding/sound-streamer.png", "image/png"
		case "/style.css":
			name, contentType = "web/style.css", "text/css; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		b, err := assets.ReadFile(name)
		if err != nil {
			http.Error(w, "asset unavailable", 500)
			return
		}
		if r.URL.Path == "/control" {
			start, end := bytes.Index(b, []byte("<!-- admin-capture:start -->")), bytes.Index(b, []byte("<!-- admin-capture:end -->"))
			if start >= 0 && end >= start {
				b = append(b[:start], b[end+len("<!-- admin-capture:end -->"):]...)
			}
			b = []byte(strings.Replace(string(b), "<body>", "<body class=\"control-mode\">", 1))
		}
		if strings.HasSuffix(name, ".html") {
			b = []byte(strings.ReplaceAll(string(b), "{{APP_VERSION}}", appVersion))
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	})
	return mux
}

func (s *station) stream(w http.ResponseWriter, r *http.Request) {
	if s.app != nil {
		roomID := r.URL.Query().Get("room")
		if !validRoomID(roomID) {
			roomID = randomID()
		}
		s.app.roomConnection(roomID, 1)
		defer s.app.roomConnection(roomID, -1)
	}
	ch := make(chan []byte, 25)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.clients, ch); s.mu.Unlock() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	format := audioFormat{SampleRate: sampleRate, Channels: 1}
	if s.app != nil {
		format = s.app.audio
	}
	w.Header().Set("X-Audio-Sample-Rate", strconv.Itoa(format.SampleRate))
	w.Header().Set("X-Audio-Channels", strconv.Itoa(format.Channels))
	w.Header().Set("X-Audio-Protocol", "2")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
				return
			}
			if _, err := w.Write(b); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		showDesktopError(err.Error())
		os.Exit(1)
	}
}

func browserBaseURL(address net.Addr, secure bool) string {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(address.String())
	if err != nil {
		return scheme + "://127.0.0.1:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func run() error {
	addr := flag.String("addr", "", "HTTP listen address override (default: saved port)")
	file := flag.String("file", "", "optional audio file to start and repeat")
	video := flag.String("video", "", "optional video file to prepare, start and repeat")
	delay := flag.Duration("buffer", 1500*time.Millisecond, "shared playout buffer (500ms to 5s)")
	dataDir := flag.String("data-dir", "", "config/cache directory (default: beside the executable)")
	flag.Parse()
	if *delay < 500*time.Millisecond || *delay > 5*time.Second {
		return fmt.Errorf("buffer must be between 500ms and 5s")
	}
	if *file != "" && *video != "" {
		return fmt.Errorf("choose either -file or -video")
	}
	if *dataDir == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		*dataDir = filepath.Dir(executable)
	}
	release, err := acquireDesktopInstance(*dataDir)
	if err != nil {
		return err
	}
	defer release()
	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		return err
	}
	logPath := filepath.Join(*dataDir, "soundstreamer.log")
	if info, err := os.Stat(logPath); err == nil && info.Size() > 5<<20 {
		os.Remove(logPath + ".1")
		os.Rename(logPath, logPath+".1")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open application log: %w", err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	if runtime.GOOS == "windows" {
		os.Stdout = logFile
		os.Stderr = logFile
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	app, err := newApplication(ctx, *dataDir, *delay)
	if err != nil {
		return err
	}
	defer func() { stop(); app.shutdown() }()
	tlsConfig, err := serverTLSConfig(app.root, app.config)
	if err != nil {
		return fmt.Errorf("HTTPS startup failed: %w. Correct certFile/keyFile or set httpsEnabled to false in soundstreamer.json while stopped", err)
	}
	app.activeHTTPS = tlsConfig != nil
	address := *addr
	if address == "" {
		address = ":" + strconv.Itoa(app.config.ListenPort)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("could not listen on %s: %w. Change the saved port in soundstreamer.json if necessary", address, err)
	}
	defer listener.Close()
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	app.activePort, _ = strconv.Atoi(portText)
	server := &http.Server{TLSConfig: tlsConfig, Handler: app.station.handler(), ReadHeaderTimeout: 5 * time.Second}
	baseURL := configuredBrowserURL(listener.Addr(), app.config)
	closeTray, err := startDesktop(app, baseURL, stop)
	if err != nil {
		return err
	}
	defer closeTray()
	app.background()
	path := *file
	if *video != "" {
		path = *video
	}
	if path != "" {
		m, _, err := probeMedia(ctx, path)
		if err != nil {
			return err
		}
		app.mu.Lock()
		app.library[m.ID] = m
		app.config.Repeat = "one"
		app.mu.Unlock()
		app.play(m, 0)
	}
	log.Printf("Sound Streamer %s listening on %s — /stream, /control, /admin", appVersion, listener.Addr())
	log.Printf("Browser URL: %s", baseURL)
	log.Printf("Configuration: %s", filepath.Join(*dataDir, "soundstreamer.json"))
	serverErrors := make(chan error, 1)
	go func() {
		var err error
		if app.activeHTTPS {
			err = server.ServeTLS(listener, "", "")
		} else {
			err = server.Serve(listener)
		}
		if err != nil && err != http.ErrServerClosed {
			serverErrors <- err
			stop()
		}
	}()
	<-ctx.Done()
	app.shutdown()
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
	server.Close()
	log.Print("Sound Streamer stopped")
	select {
	case err := <-serverErrors:
		return err
	default:
		return nil
	}
}
