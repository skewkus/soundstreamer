package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type media struct {
	Category     string  `json:"category,omitempty"`
	CategoryName string  `json:"categoryName,omitempty"`
	ID           string  `json:"id"`
	Path         string  `json:"path"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist,omitempty"`
	Album        string  `json:"album,omitempty"`
	Duration     float64 `json:"duration"`
}

type mediaProbe struct {
	Format struct {
		Duration string            `json:"duration"`
		Name     string            `json:"format_name"`
		Start    string            `json:"start_time"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		Type        string            `json:"codec_type"`
		Codec       string            `json:"codec_name"`
		PixelFormat string            `json:"pix_fmt"`
		Start       string            `json:"start_time"`
		Duration    string            `json:"duration"`
		Tags        map[string]string `json:"tags"`
	} `json:"streams"`
}

func identifier(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func probeMedia(ctx context.Context, path string) (media, mediaProbe, error) {
	m := media{}
	var p mediaProbe
	abs, err := filepath.Abs(path)
	if err != nil {
		return m, p, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return m, p, fmt.Errorf("media file is missing or unreadable")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := backgroundCommand(probeCtx, "ffprobe", "-v", "error", "-show_entries", "format=duration,format_name,start_time:format_tags=title,artist,album:stream=codec_type,codec_name,pix_fmt,start_time,duration:stream_tags=title,artist,album", "-of", "json", abs).Output()
	if err != nil {
		return m, p, fmt.Errorf("cannot inspect %s; check ffprobe and the media format", filepath.Base(abs))
	}
	if err := json.Unmarshal(output, &p); err != nil {
		return m, p, err
	}
	duration, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return m, p, fmt.Errorf("media has no valid duration")
	}
	kind := ""
	for _, stream := range p.Streams {
		if stream.Type == "audio" && kind == "" {
			kind = "audio"
		}
		if stream.Type == "video" && stream.Codec != "mjpeg" && stream.Codec != "png" {
			kind = "video"
		}
	}
	if kind == "" {
		return m, p, fmt.Errorf("file has no playable audio or video")
	}
	tags := map[string]string{}
	for k, v := range p.Format.Tags {
		tags[strings.ToLower(k)] = v
	}
	for _, stream := range p.Streams {
		for k, v := range stream.Tags {
			if tags[strings.ToLower(k)] == "" {
				tags[strings.ToLower(k)] = v
			}
		}
	}
	title := tags["title"]
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	}
	m = media{ID: identifier(abs), Path: abs, Kind: kind, Title: title, Artist: tags["artist"], Album: tags["album"], Duration: duration}
	return m, p, nil
}

func nativeVideo(p mediaProbe) bool {
	if !strings.Contains(p.Format.Name, "mp4") {
		return false
	}
	hasAudio, hasVideo := false, false
	for _, s := range p.Streams {
		if s.Type == "audio" {
			if s.Codec != "aac" {
				return false
			}
			hasAudio = true
		}
		if s.Type == "video" && !hasVideo {
			start, err := strconv.ParseFloat(s.Start, 64)
			if s.Codec != "h264" || s.PixelFormat != "yuv420p" || err != nil || math.IsNaN(start) || math.Abs(start) > 0.02 {
				return false
			}
			hasVideo = true
		}
	}
	return hasVideo && hasAudio
}

// prepareVideo describes the source; incompatible files are encoded in short chunks on demand.
func prepareVideo(ctx context.Context, root string, source media, probe mediaProbe, progress func(float64)) (*videoSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(source.Path)
	if err != nil {
		return nil, err
	}
	video := &videoSource{Path: source.Path, Duration: source.Duration, Transcode: !nativeVideo(probe), Key: identifier(fmt.Sprintf("live-v1|%s|%d|%d", source.Path, info.Size(), info.ModTime().UnixNano()))}
	for _, s := range probe.Streams {
		if s.Type == "audio" {
			video.HasAudio = true
		}
		if s.Type == "video" && video.VisualDuration == 0 {
			if d, err := strconv.ParseFloat(s.Duration, 64); err == nil && d > 0 && !math.IsInf(d, 0) {
				video.VisualDuration = math.Min(d, source.Duration)
			}
		}
	}
	progress(100)
	return video, nil
}
