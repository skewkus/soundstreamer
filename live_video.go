package main

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"
)

const videoChunkSeconds = 4.0
const maxVideoCacheBytes = 64 << 20
const maxVideoChunkBytes = 24 << 20
const maxVideoCacheEntries = 16

type encodedVideo struct {
	Data  []byte
	Codec string
}
type videoJob struct {
	done   chan struct{}
	result *encodedVideo
	err    error
	used   time.Time
}
type videoChunks struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	closed  bool
	jobs    map[string]*videoJob
	slots   chan struct{}
	workers sync.WaitGroup
}

func newVideoChunks(parent context.Context) *videoChunks {
	ctx, cancel := context.WithCancel(parent)
	return &videoChunks{ctx: ctx, cancel: cancel, jobs: map[string]*videoJob{}, slots: make(chan struct{}, 2)}
}

func (v *videoChunks) get(ctx context.Context, source *videoSource, index int) (*encodedVideo, error) {
	if index < 0 || float64(index)*videoChunkSeconds >= source.pictureDuration() {
		return nil, fmt.Errorf("invalid video chunk")
	}
	key := source.Key + ":" + strconv.Itoa(index)
	v.mu.Lock()
	if v.closed || v.ctx.Err() != nil {
		v.mu.Unlock()
		return nil, context.Canceled
	}
	job := v.jobs[key]
	if job == nil {
		// In-flight work is shared across rooms and bounded separately from cached bytes.
		pending := 0
		for _, j := range v.jobs {
			select {
			case <-j.done:
			default:
				pending++
			}
		}
		if pending >= 8 {
			v.mu.Unlock()
			return nil, fmt.Errorf("video encoder busy; retry shortly")
		}
		job = &videoJob{done: make(chan struct{}), used: time.Now()}
		v.jobs[key] = job
		v.workers.Add(1)
		go func() {
			defer v.workers.Done()
			var result *encodedVideo
			var err error
			select {
			case v.slots <- struct{}{}:
				encodeCtx, cancel := context.WithTimeout(v.ctx, 60*time.Second)
				result, err = encodeVideoChunk(encodeCtx, source, index)
				cancel()
				<-v.slots
			case <-v.ctx.Done():
				err = v.ctx.Err()
			}
			v.mu.Lock()
			job.result, job.err = result, err
			close(job.done)
			if err != nil {
				delete(v.jobs, key)
			}
			v.trimLocked()
			v.mu.Unlock()
		}()
	}
	job.used = time.Now()
	v.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-job.done:
		return job.result, job.err
	}
}

func (v *videoChunks) trimLocked() {
	for {
		count, total := 0, 0
		oldKey := ""
		var oldest time.Time
		for key, j := range v.jobs {
			select {
			case <-j.done:
				if j.result != nil {
					count++
					total += len(j.result.Data)
					if oldKey == "" || j.used.Before(oldest) {
						oldKey, oldest = key, j.used
					}
				}
			default:
			}
		}
		if count <= maxVideoCacheEntries && total <= maxVideoCacheBytes {
			return
		}
		delete(v.jobs, oldKey)
	}
}

func (v *videoChunks) close() {
	v.mu.Lock()
	v.closed = true
	v.cancel()
	v.mu.Unlock()
	v.workers.Wait()
	v.mu.Lock()
	v.jobs = map[string]*videoJob{}
	v.mu.Unlock()
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("encoded video chunk exceeds memory limit")
	}
	return b.Buffer.Write(p)
}

func encodeVideoChunk(ctx context.Context, source *videoSource, index int) (*encodedVideo, error) {
	start := float64(index) * videoChunkSeconds
	duration := math.Min(videoChunkSeconds, source.pictureDuration()-start)
	preset := videoTranscodePresetFor(source.TranscodeProfile)
	filter := "setpts=PTS-STARTPTS,scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p"
	if preset.Height > 0 {
		filter = fmt.Sprintf("setpts=PTS-STARTPTS,scale=w='min(iw,%d)':h='min(ih,%d)':force_original_aspect_ratio=decrease:force_divisible_by=2,format=yuv420p", preset.Width, preset.Height)
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-ss", strconv.FormatFloat(start, 'f', 6, 64), "-i", source.Path, "-map", "0:v:0", "-an", "-vf", filter, "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "23", "-maxrate", preset.MaxRate, "-bufsize", preset.BufferSize, "-force_key_frames", "expr:gte(t,n_forced*1)", "-t", strconv.FormatFloat(duration, 'f', 6, 64), "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1"}
	cmd := backgroundCommand(ctx, "ffmpeg", args...)
	output := &boundedBuffer{limit: maxVideoChunkBytes}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout = output
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		message := stderr.String()
		if len(message) > 1200 {
			message = message[len(message)-1200:]
		}
		return nil, fmt.Errorf("live video transcoding failed: %s", message)
	}
	data := output.Bytes()
	indexAVCC := bytes.Index(data, []byte("avcC"))
	if indexAVCC < 0 || indexAVCC+8 > len(data) || data[indexAVCC+4] != 1 || !bytes.Contains(data, []byte("moof")) {
		return nil, fmt.Errorf("encoder produced no playable video frames")
	}
	codec := fmt.Sprintf("avc1.%02X%02X%02X", data[indexAVCC+5], data[indexAVCC+6], data[indexAVCC+7])
	return &encodedVideo{Data: data, Codec: codec}, nil
}
