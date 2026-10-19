package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const desktopChunkSeconds = 0.5
const desktopCacheBytes = 32 << 20

// Maintain at least 500ms of delivery lead when advancing live audio. Extra
// shared buffering is needed only when the requested advance exceeds the
// existing buffer; the audio/picture difference remains exactly the offset.
func screenAudioPadding(buffer, offset time.Duration) time.Duration {
	padding := 500*time.Millisecond - buffer - offset
	if padding > 0 {
		return padding
	}
	return 0
}
func (a *application) screenAudioPlayoutLocked(at time.Time) time.Duration {
	offset := time.Duration(a.config.ScreenAudioOffsetMs) * time.Millisecond
	return at.Add(a.station.delay + screenAudioPadding(a.station.delay, offset) + offset).Sub(a.station.origin)
}

type desktopFragment struct {
	Index int
	Start float64
	Data  []byte
}
type desktopStream struct {
	mu          sync.Mutex
	init        []byte
	codec       string
	timescale   uint32
	fragments   []desktopFragment
	next, bytes int
	closed      bool
	updated     time.Time
}

func (d *desktopStream) close() { d.mu.Lock(); d.closed = true; d.mu.Unlock() }
func (d *desktopStream) add(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return context.Canceled
	}
	at := bytes.Index(data, []byte("tfdt"))
	if at < 0 || at+12 > len(data) || d.timescale == 0 {
		return fmt.Errorf("invalid desktop fragment timestamps")
	}
	var pts uint64
	if data[at+4] == 1 {
		if at+16 > len(data) {
			return io.ErrUnexpectedEOF
		}
		pts = binary.BigEndian.Uint64(data[at+8:])
	} else {
		pts = uint64(binary.BigEndian.Uint32(data[at+8:]))
	}
	d.fragments = append(d.fragments, desktopFragment{d.next, float64(pts) / float64(d.timescale), data})
	d.next++
	d.bytes += len(data)
	d.updated = time.Now()
	for len(d.fragments) > 16 || d.bytes > desktopCacheBytes {
		d.bytes -= len(d.fragments[0].Data)
		d.fragments = d.fragments[1:]
	}
	return nil
}
func readMP4Box(r io.Reader) ([]byte, string, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, "", err
	}
	size := uint64(binary.BigEndian.Uint32(header))
	kind := string(header[4:])
	if size == 1 {
		extra := make([]byte, 8)
		if _, err := io.ReadFull(r, extra); err != nil {
			return nil, "", err
		}
		header = append(header, extra...)
		size = binary.BigEndian.Uint64(extra)
	}
	if size < uint64(len(header)) || size > 8<<20 {
		return nil, "", fmt.Errorf("invalid or oversized desktop MP4 box")
	}
	box := make([]byte, int(size))
	copy(box, header)
	_, err := io.ReadFull(r, box[len(header):])
	return box, kind, err
}
func (d *desktopStream) read(r io.Reader) error {
	var fragment []byte
	for {
		box, kind, err := readMP4Box(r)
		if err != nil {
			return err
		}
		switch kind {
		case "ftyp":
			d.mu.Lock()
			d.init = box
			d.mu.Unlock()
		case "moov":
			avcc := bytes.Index(box, []byte("avcC"))
			mdhd := bytes.Index(box, []byte("mdhd"))
			if avcc < 0 || avcc+8 > len(box) || mdhd < 0 || mdhd+28 > len(box) {
				return fmt.Errorf("desktop encoder produced invalid initialization")
			}
			offset := mdhd + 16
			if box[mdhd+4] == 1 {
				offset = mdhd + 24
			}
			d.mu.Lock()
			d.init = append(d.init, box...)
			d.codec = fmt.Sprintf("avc1.%02X%02X%02X", box[avcc+5], box[avcc+6], box[avcc+7])
			d.timescale = binary.BigEndian.Uint32(box[offset:])
			d.mu.Unlock()
		case "moof":
			fragment = box
		case "mdat":
			if len(fragment) == 0 {
				return fmt.Errorf("desktop video data has no timing fragment")
			}
			fragment = append(fragment, box...)
			if err := d.add(fragment); err != nil {
				return err
			}
			fragment = nil
		}
	}
}
func desktopInputArgs(fps int) ([]string, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("PC screen capture currently requires Windows")
	}
	// Capture the primary monitor in physical pixels, then scale for delivery.
	args := []string{"-thread_queue_size", "512", "-use_wallclock_as_timestamps", "1", "-f", "gdigrab", "-framerate", strconv.Itoa(fps)}
	args = append(args, desktopRegionArgs()...)
	return append(args, "-i", "desktop"), nil
}
func desktopEncoderArgs(input []string, fps int, epoch time.Time, address string, format audioFormat) []string {
	origin := strconv.FormatFloat(float64(epoch.UnixMicro())/1e6, 'f', 6, 64)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-copyts"}
	args = append(args, input...)
	args = append(args, "-thread_queue_size", "512", "-use_wallclock_as_timestamps", "1", "-probesize", "4096", "-analyzeduration", "0", "-f", "wav", "-i", "pipe:0")
	args = append(args, "-map", "0:v:0", "-an", "-vf", "setpts=PTS-"+origin+"/TB,fps="+strconv.Itoa(fps)+",scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,format=yuv420p", "-fps_mode", "passthrough", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "23", "-maxrate", "12M", "-bufsize", "12M", "-g", strconv.Itoa(fps/2), "-keyint_min", strconv.Itoa(fps/2), "-sc_threshold", "0", "-bf", "0", "-avoid_negative_ts", "disabled", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-flush_packets", "1", "-f", "mp4", "pipe:1")
	return append(args, "-map", "1:a:0", "-vn", "-af", "asetpts=PTS-"+origin+"/TB,aresample="+strconv.Itoa(format.SampleRate)+":async=1000:first_pts=0", "-ac", strconv.Itoa(format.Channels), "-ar", strconv.Itoa(format.SampleRate), "-c:a", "pcm_s16le", "-f", "s16le", "tcp://"+address)
}
func (a *application) startPCScreen() error {
	a.mu.Lock()
	fps := a.config.ScreenFPS
	a.mu.Unlock()
	inputArgs, err := a.desktopInput(fps)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	capture, err := a.pcAudioSource(ctx)
	if err != nil {
		cancel()
		return err
	}
	input, err := capture.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		input.Close()
		return err
	}
	epoch := time.Now()
	encoder := backgroundCommand(ctx, "ffmpeg", desktopEncoderArgs(inputArgs, fps, epoch, listener.Addr().String(), a.audio)...)
	encoder.Stdin = input
	output, err := encoder.StdoutPipe()
	if err != nil {
		cancel()
		listener.Close()
		input.Close()
		return err
	}
	stream := &desktopStream{updated: time.Now()}
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		cancel()
		listener.Close()
		input.Close()
		output.Close()
		return fmt.Errorf("server shutting down")
	}
	if !a.pcAudio {
		a.position = a.positionLocked(time.Now())
	}
	a.config.Position = a.position
	a.cancelLocked()
	a.pcAudio = true
	a.pcScreen = stream
	a.video = nil
	a.loading = true
	a.lastError = ""
	generation := a.generation
	a.cancelPlayback = cancel
	a.workers.Add(1)
	a.station.resetStreams()
	a.mu.Unlock()
	a.save()
	go a.runPCScreen(ctx, cancel, generation, stream, capture, encoder, input, output, listener)
	return nil
}
func (a *application) runPCScreen(ctx context.Context, cancel context.CancelFunc, generation uint64, stream *desktopStream, capture, encoder *exec.Cmd, input, output io.ReadCloser, listener net.Listener) {
	format := a.audio
	defer a.workers.Done()
	defer cancel()
	defer listener.Close()
	defer input.Close()
	defer output.Close()
	defer stream.close()
	errors := &boundedBuffer{limit: 64 << 10}
	captureErrors := &boundedBuffer{limit: 64 << 10}
	encoder.Stderr = errors
	capture.Stderr = captureErrors
	fail := func(err error) {
		if ctx.Err() == nil {
			a.playbackError(generation, ctx, err)
		}
		cancel()
	}
	if err := capture.Start(); err != nil {
		fail(fmt.Errorf("start screen audio: %w", err))
		return
	}
	if err := encoder.Start(); err != nil {
		cancel()
		capture.Wait()
		a.playbackError(generation, ctx, fmt.Errorf("start desktop encoder: %w", err))
		return
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		err := stream.read(output)
		if ctx.Err() == nil && err != nil {
			fail(fmt.Errorf("screen capture ended: %w", err))
		}
	}()
	go func() { defer workers.Done(); <-ctx.Done(); listener.Close() }()
	listener.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Second))
	connection, err := listener.Accept()
	if err != nil {
		fail(fmt.Errorf("desktop audio did not start: %w", err))
	} else {
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				connection.Close()
			case <-done:
			}
		}()
		start := time.Now()
		frame := int64(0)
		anchored := false
		for ctx.Err() == nil {
			connection.SetReadDeadline(time.Now().Add(15 * time.Second))
			pcm := make([]byte, format.chunkBytes())
			if _, err := io.ReadFull(connection, pcm); err != nil {
				fail(fmt.Errorf("screen audio capture ended: %w", err))
				break
			}
			if !anchored {
				start = time.Now()
				anchored = true
				a.mu.Lock()
				if a.generation == generation {
					a.screenAnchorBaseMs = float64(start.Add(a.station.delay).Sub(a.station.origin)) / float64(time.Millisecond)
					a.anchorMs = a.screenAnchorBaseMs + float64(screenAudioPadding(a.station.delay, time.Duration(a.config.ScreenAudioOffsetMs)*time.Millisecond))/float64(time.Millisecond)
					a.loading = false
					a.playing = true
					a.station.resetStreams()
				}
				a.mu.Unlock()
				a.save()
			}
			at := start.Add(time.Duration(frame) * time.Second / time.Duration(format.SampleRate))
			timer := time.NewTimer(time.Until(at))
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			a.mu.Lock()
			if a.generation != generation || ctx.Err() != nil {
				a.mu.Unlock()
				break
			}
			a.station.publish(format.packet(a.screenAudioPlayoutLocked(at), pcm))
			a.mu.Unlock()
			frame += int64(format.chunkFrames())
		}
		close(done)
		connection.Close()
	}
	cancel()
	encoder.Wait()
	capture.Wait()
	workers.Wait()
	if ctx.Err() != nil && a.ctx.Err() == nil {
		// Child diagnostics are useful for unsupported capture builds or encoder failures.
		message := strings.TrimSpace(captureErrors.String() + " " + errors.String())
		if message != "" {
			if len(message) > 1200 {
				message = message[len(message)-1200:]
			}
			a.mu.Lock()
			if a.generation == generation && a.lastError != "" {
				a.lastError += " · " + message
			}
			a.mu.Unlock()
			a.save()
		}
	}
}
func (a *application) desktopRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/desktop", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		stream, session := a.pcScreen, a.session
		a.mu.Unlock()
		if r.URL.Query().Get("session") != session {
			http.Error(w, "Screen session changed", 409)
			return
		}
		if stream == nil {
			http.NotFound(w, r)
			return
		}
		stream.mu.Lock()
		w.Header().Set("Cache-Control", "no-store")
		if stream.closed {
			stream.mu.Unlock()
			http.Error(w, "Screen capture stopped", 409)
			return
		}
		if r.URL.Query().Get("info") == "1" {
			if len(stream.fragments) == 0 {
				stream.mu.Unlock()
				http.Error(w, "Screen capture is buffering", 503)
				return
			}
			first, last := stream.fragments[0], stream.fragments[len(stream.fragments)-1]
			codec := stream.codec
			stream.mu.Unlock()
			respond(w, map[string]any{"first": first.Index, "last": last.Index, "firstTime": first.Start, "lastTime": last.Start + desktopChunkSeconds, "codec": codec})
			return
		}
		index, err := strconv.Atoi(r.URL.Query().Get("segment"))
		if err != nil || index < 0 {
			stream.mu.Unlock()
			http.Error(w, "Invalid screen fragment", 400)
			return
		}
		for _, fragment := range stream.fragments {
			if fragment.Index == index {
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("X-Video-Codec", stream.codec)
				data := append(append([]byte{}, stream.init...), fragment.Data...)
				// Release the cache lock before writing to a slow room.
				stream.mu.Unlock()
				http.ServeContent(w, r, "desktop.mp4", time.Time{}, bytes.NewReader(data))
				return
			}
		}
		stream.mu.Unlock()
		http.Error(w, "Screen fragment expired or not yet available", 410)
	})
}
