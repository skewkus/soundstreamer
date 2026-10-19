package main

import (
	"encoding/binary"
	"math"
	"time"
)

// PCM is signed 16-bit little-endian, interleaved by frame. A packet contains
// exactly 20 ms at every supported rate; its header counts frames, not samples.
type audioFormat struct{ SampleRate, Channels int }

func validAudioSampleRate(rate int) bool {
	switch rate {
	case 48000, 44100, 32000, 24000:
		return true
	}
	return false
}
func (f audioFormat) chunkFrames() int { return f.SampleRate / 50 }
func (f audioFormat) chunkBytes() int  { return f.chunkFrames() * f.Channels * 2 }
func (f audioFormat) channelLayout() string {
	if f.Channels == 2 {
		return "stereo"
	}
	return "mono"
}
func (f audioFormat) packet(at time.Duration, pcm []byte) []byte {
	b := make([]byte, 12+len(pcm))
	binary.LittleEndian.PutUint64(b, math.Float64bits(float64(at)/float64(time.Millisecond)))
	binary.LittleEndian.PutUint32(b[8:], uint32(len(pcm)/(2*f.Channels)))
	copy(b[12:], pcm)
	return b
}
