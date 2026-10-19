package main

func validVideoTranscodeResolution(value string) bool {
	switch value {
	case "original", "1080p", "720p", "480p":
		return true
	}
	return false
}

type videoTranscodePreset struct {
	Width, Height       int
	MaxRate, BufferSize string
}

func videoTranscodePresetFor(resolution string) videoTranscodePreset {
	switch resolution {
	case "1080p":
		return videoTranscodePreset{1920, 1080, "8M", "16M"}
	case "720p":
		return videoTranscodePreset{1280, 720, "4M", "8M"}
	case "480p":
		return videoTranscodePreset{854, 480, "2M", "4M"}
	default:
		return videoTranscodePreset{0, 0, "24M", "48M"}
	}
}

// A source is immutable once shared with encoder jobs. Return a copy for forced
// profiles so an in-flight job can never observe another playback's settings.
func forcedVideoSource(source *videoSource, force bool, resolution string) *videoSource {
	if !force {
		return source
	}
	result := *source
	result.Transcode = true
	result.TranscodeProfile = resolution
	result.Key = identifier(source.Key + "|forced-v1|" + resolution)
	return &result
}
