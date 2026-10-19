package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestScreenFPSSettingPersistsAndIsAdminOnly(t *testing.T) {
	a := testApplication(t)
	if a.config.ScreenFPS != 30 {
		t.Fatal("default screen FPS")
	}
	for _, fps := range []int{60, 30} {
		if err := a.action(actionRequest{Action: "screenFPS", To: fps}); err != nil {
			t.Fatal(err)
		}
		saved, err := loadSettings(a.root)
		if err != nil || saved.ScreenFPS != fps {
			t.Fatal("FPS not persisted")
		}
	}
	if err := a.action(actionRequest{Action: "screenFPS", To: 120}); err == nil {
		t.Fatal("invalid FPS accepted")
	}
	r := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/control/action", bytes.NewBufferString(`{"action":"screenFPS","to":60}`))
	request.Header.Set("Content-Type", "application/json")
	a.station.handler().ServeHTTP(r, request)
	if r.Code != 403 {
		t.Fatal("public settings changed")
	}
}
func TestDesktopCaptureLiveFragmentsAndSharedPCM(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	for _, fps := range []int{30, 60} {
		t.Run(strconv.Itoa(fps), func(t *testing.T) {
			a := testApplication(t)
			a.config.ScreenFPS = fps
			if fps == 60 {
				a.audio = audioFormat{SampleRate: 44100, Channels: 1}
			}
			a.pcAudioSource = func(ctx context.Context) (*exec.Cmd, error) {
				return exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-re", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-c:a", "pcm_s16le", "-f", "wav", "pipe:1"), nil
			}
			a.desktopInput = func(rate int) ([]string, error) {
				if rate != fps {
					t.Fatal("selected FPS not passed to capture")
				}
				return []string{"-use_wallclock_as_timestamps", "1", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=" + strconv.Itoa(rate)}, nil
			}
			if err := a.action(actionRequest{Action: "pcScreen"}); err != nil {
				t.Fatal(err)
			}
			waitPlaybackState(t, a, func() bool { return a.playing || a.lastError != "" })
			a.mu.Lock()
			failure, session, stream := a.lastError, a.session, a.pcScreen
			a.mu.Unlock()
			if failure != "" {
				t.Fatal(failure)
			}
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				stream.mu.Lock()
				count := len(stream.fragments)
				stream.mu.Unlock()
				if count >= 3 {
					break
				}
				time.Sleep(30 * time.Millisecond)
			}
			stream.mu.Lock()
			if len(stream.fragments) < 3 {
				stream.mu.Unlock()
				a.mu.Lock()
				failure = a.lastError
				a.mu.Unlock()
				t.Fatalf("missing screen fragments: %s", failure)
			}
			first, second := stream.fragments[0], stream.fragments[1]
			codec := stream.codec
			init := append([]byte{}, stream.init...)
			stream.mu.Unlock()
			t.Logf("fps=%d first=%.4f second=%.4f codec=%s", fps, first.Start, second.Start, codec)
			if first.Start < 0 || first.Start > 3 || math.Abs(second.Start-first.Start-.5) > .05 {
				t.Fatalf("screen timeline incorrect: %+v %+v", first, second)
			}
			h := a.station.handler()
			get := func(query string) *httptest.ResponseRecorder {
				r := httptest.NewRecorder()
				h.ServeHTTP(r, httptest.NewRequest("GET", "/api/desktop?session="+session+query, nil))
				return r
			}
			info := get("&info=1")
			if info.Code != 200 {
				t.Fatal(info.Body.String())
			}
			var manifest map[string]any
			if err := json.Unmarshal(info.Body.Bytes(), &manifest); err != nil {
				t.Fatal(err)
			}
			left, right := get("&segment=0"), get("&segment=0")
			if left.Code != 200 || !bytes.Equal(left.Body.Bytes(), right.Body.Bytes()) || !bytes.HasPrefix(left.Body.Bytes(), init) || !bytes.Contains(left.Body.Bytes(), []byte("moof")) {
				t.Fatal("shared screen fragment missing")
			}
			probe := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height,r_frame_rate", "-of", "json", "-i", "pipe:0")
			probe.Stdin = bytes.NewReader(left.Body.Bytes())
			metadata, err := probe.Output()
			if err != nil {
				t.Fatal(err)
			}
			var details struct {
				Streams []struct {
					Width  int
					Height int
					FPS    string `json:"r_frame_rate"`
				}
			}
			if err := json.Unmarshal(metadata, &details); err != nil {
				t.Fatal(err)
			}
			if len(details.Streams) != 1 || details.Streams[0].Width != 1280 || details.Streams[0].Height != 720 || details.Streams[0].FPS != strconv.Itoa(fps)+"/1" {
				t.Fatalf("incorrect encoded picture: %s", metadata)
			}
			invalid := get("&segment=-1")
			if invalid.Code != 400 {
				t.Fatal("negative fragment accepted")
			}
			one, two := make(chan []byte, 25), make(chan []byte, 25)
			a.station.mu.Lock()
			a.station.clients[one] = struct{}{}
			a.station.clients[two] = struct{}{}
			a.station.mu.Unlock()
			var prev float64
			for i := 0; i < 5; i++ {
				var x, y []byte
				select {
				case x = <-one:
				case <-time.After(3 * time.Second):
					t.Fatal("no PCM")
				}
				select {
				case y = <-two:
				case <-time.After(3 * time.Second):
					t.Fatal("no second room PCM")
				}
				if len(x) != 12+a.audio.chunkBytes() || int(binary.LittleEndian.Uint32(x[8:])) != a.audio.chunkFrames() {
					t.Fatal("screen capture audio format incorrect")
				}
				if !bytes.Equal(x, y) {
					t.Fatal("room audio differs")
				}
				pts := math.Float64frombits(binary.LittleEndian.Uint64(x[:8]))
				if i > 0 && math.Abs(pts-prev-20) > .01 {
					t.Fatal("PCM timeline unstable")
				}
				prev = pts
			}
			// Apply the offset during the live capture and reconnect both rooms.
			previousClock := a.clock()
			oldAnchor := previousClock["playback"].(map[string]any)["anchorMs"]
			a.mu.Lock()
			originalStream, originalGeneration := a.pcScreen, a.generation
			a.mu.Unlock()
			offset := 1000
			if fps == 60 {
				offset = -1000
			}
			if err := a.action(actionRequest{Action: "screenAudioOffset", To: offset}); err != nil {
				t.Fatal(err)
			}
			one, two = make(chan []byte, 25), make(chan []byte, 25)
			a.station.mu.Lock()
			a.station.clients[one] = struct{}{}
			a.station.clients[two] = struct{}{}
			a.station.mu.Unlock()
			var adjusted, secondRoom []byte
			select {
			case adjusted = <-one:
			case <-time.After(3 * time.Second):
				t.Fatal("no adjusted audio")
			}
			select {
			case secondRoom = <-two:
			case <-time.After(3 * time.Second):
				t.Fatal("no adjusted audio in second room")
			}
			if !bytes.Equal(adjusted, secondRoom) {
				t.Fatal("offset separated rooms")
			}
			shifted := math.Float64frombits(binary.LittleEndian.Uint64(adjusted[:8]))
			if shifted-prev-float64(offset) < -0.01 || shifted-prev-float64(offset) > 250 {
				t.Fatalf("live offset did not shift audio timestamps: %.2f", shifted-prev)
			}
			a.mu.Lock()
			if a.pcScreen != originalStream || a.generation != originalGeneration {
				t.Fatal("offset restarted encoder")
			}
			a.mu.Unlock()
			clock := a.clock()
			if clock["playback"].(map[string]any)["anchorMs"] != oldAnchor {
				t.Fatal("one-second audio offset moved the video clock")
			}
			session = clock["session"].(string)
			if clock["mode"] != "video" || clock["video"].(map[string]any)["live"] != true {
				t.Fatal("unified stream does not describe live video")
			}
			if err := a.action(actionRequest{Action: "pause"}); err != nil {
				t.Fatal(err)
			}
			a.workers.Wait()
			if a.pcScreen != nil || a.pcAudio || a.playing {
				t.Fatal("screen capture did not stop")
			}
			if get("&segment=0").Code != 409 {
				t.Fatal("old session still available")
			}
		})
	}
}

func TestDesktopCacheEvictsAndRejectsOversizedBoxes(t *testing.T) {
	stream := &desktopStream{timescale: 1000}
	// A minimal fragment timing box is sufficient to exercise rolling retention.
	fragment := make([]byte, 20)
	copy(fragment[4:], "tfdt")
	for i := 0; i < 40; i++ {
		binary.BigEndian.PutUint32(fragment[12:], uint32(i*500))
		if err := stream.add(append([]byte{}, fragment...)); err != nil {
			t.Fatal(err)
		}
	}
	if len(stream.fragments) != 16 || stream.fragments[0].Index != 24 || stream.bytes > desktopCacheBytes {
		t.Fatal("live cache did not bound history")
	}
	stream.close()
	if err := stream.add(fragment); err == nil {
		t.Fatal("closed stream accepted frames")
	}
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header, 100<<20)
	copy(header[4:], "mdat")
	if _, _, err := readMP4Box(bytes.NewReader(header)); err == nil {
		t.Fatal("oversized box accepted")
	}
}
