package main

type videoSource struct {
	TranscodeProfile string
	Path             string
	Duration         float64
	VisualDuration   float64
	Transcode        bool
	HasAudio         bool
	Key              string
}

func (v *videoSource) pictureDuration() float64 {
	if v.VisualDuration > 0 {
		return v.VisualDuration
	}
	return v.Duration
}
