package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

//go:embed capture/loopback.ps1
var loopbackScript string

func pcAudioCommand(ctx context.Context) (*exec.Cmd, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("PC audio capture currently requires Windows")
	}
	encoded := utf16.Encode([]rune(loopbackScript))
	data := make([]byte, len(encoded)*2)
	for i, v := range encoded {
		binary.LittleEndian.PutUint16(data[i*2:], v)
	}
	return backgroundCommand(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data)), nil
}

// startPCAudio keeps the library selection and queue, so Play resumes the selected
// track after capture stops. Live input is deliberately not restored on startup.
func (a *application) startPCAudio() error {
	format := a.audio
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
	decoder := backgroundCommand(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-probesize", "4096", "-analyzeduration", "0", "-f", "wav", "-i", "pipe:0", "-vn", "-f", "s16le", "-acodec", "pcm_s16le", "-ar", strconv.Itoa(format.SampleRate), "-ac", strconv.Itoa(format.Channels), "pipe:1")
	decoder.Stdin = input
	output, err := decoder.StdoutPipe()
	if err != nil {
		cancel()
		input.Close()
		return err
	}
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		cancel()
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
	a.pcAudioLastPacket = time.Now()
	a.video = nil
	a.loading = true
	a.lastError = ""
	generation := a.generation
	a.cancelPlayback = cancel
	a.workers.Add(1)
	a.station.resetStreams()
	a.mu.Unlock()
	a.save()
	go func() {
		defer a.workers.Done()
		defer cancel()
		defer input.Close()
		defer output.Close()
		var captureErrors, decoderErrors bytes.Buffer
		capture.Stderr = &captureErrors
		decoder.Stderr = &decoderErrors
		if err := capture.Start(); err != nil {
			a.playbackError(generation, ctx, fmt.Errorf("start PC audio capture: %w", err))
			return
		}
		if err := decoder.Start(); err != nil {
			cancel()
			capture.Wait()
			a.playbackError(generation, ctx, fmt.Errorf("start PC audio decoder: %w", err))
			return
		}
		// Bound setup and detect a disconnected endpoint rather than staying stuck.
		watchdogDone := make(chan struct{})
		go func() {
			defer close(watchdogDone)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-ticker.C:
					a.mu.Lock()
					stale := a.generation == generation && now.Sub(a.pcAudioLastPacket) > 15*time.Second
					a.mu.Unlock()
					if stale {
						a.playbackError(generation, ctx, fmt.Errorf("PC audio capture stopped responding; check the Windows output device"))
						cancel()
						return
					}
				}
			}
		}()
		a.mu.Lock()
		if a.generation == generation {
			a.pcAudioLastPacket = time.Now()
		}
		a.mu.Unlock()
		start := time.Now()
		first := make([]byte, format.chunkBytes())
		_, readErr := io.ReadFull(output, first)
		if readErr == nil {
			a.mu.Lock()
			if a.generation == generation && ctx.Err() == nil {
				start = time.Now()
				a.loading = false
				a.playing = true
				a.anchorMs = float64(start.Add(a.station.delay).Sub(a.station.origin)) / float64(time.Millisecond)
				a.station.resetStreams()
			}
			a.mu.Unlock()
			a.save()
		}
		// Pace fixed PCM chunks on one shared timeline, including native silence.
		// Re-anchor after a long capture stall rather than publishing a catch-up burst.
		frame := int64(0)
		pcm := first
		for readErr == nil && ctx.Err() == nil {
			at := start.Add(time.Duration(frame) * time.Second / time.Duration(format.SampleRate))
			if time.Since(at) > 200*time.Millisecond {
				start = time.Now().Add(-time.Duration(frame) * time.Second / time.Duration(format.SampleRate))
				at = time.Now()
			}
			timer := time.NewTimer(time.Until(at))
			select {
			case <-ctx.Done():
				timer.Stop()
				break
			case <-timer.C:
			}
			a.mu.Lock()
			if a.generation != generation || ctx.Err() != nil {
				a.mu.Unlock()
				break
			}
			a.pcAudioLastPacket = time.Now()
			a.station.publish(format.packet(at.Add(a.station.delay).Sub(a.station.origin), pcm))
			a.mu.Unlock()
			frame += int64(format.chunkFrames())
			pcm = make([]byte, format.chunkBytes())
			_, readErr = io.ReadFull(output, pcm)
		}
		intentional := ctx.Err() != nil
		cancel()
		decoderErr := decoder.Wait()
		captureErr := capture.Wait()
		<-watchdogDone
		if !intentional {
			detail := strings.TrimSpace(captureErrors.String() + " " + decoderErrors.String())
			if len(detail) > 1200 {
				detail = detail[:1200]
			}
			a.playbackError(generation, ctx, fmt.Errorf("PC audio capture ended (%v; %v): %s", captureErr, decoderErr, detail))
		}
	}()
	return nil
}
