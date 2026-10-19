package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func readPacket(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b := make([]byte, 12+chunkSamples*2)
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBroadcastAndLateJoin(t *testing.T) {
	s := newStation(1500*time.Millisecond, "test")
	server := httptest.NewServer(s.handler())
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	first, err := client.Get(server.URL + "/api/audio")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	second, err := client.Get(server.URL + "/api/audio")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	one := (audioFormat{SampleRate: 48000, Channels: 1}).packet(1500*time.Millisecond, bytes.Repeat([]byte{1, 0}, chunkSamples))
	s.publish(one)
	for _, body := range []io.Reader{first.Body, second.Body} {
		got := readPacket(t, body)
		if string(got) != string(one) {
			t.Fatal("rooms received different audio or timestamps")
		}
		if binary.LittleEndian.Uint32(got[8:]) != chunkSamples {
			t.Fatal("invalid sample count")
		}
		if math.Float64frombits(binary.LittleEndian.Uint64(got)) != 1500 {
			t.Fatal("wrong playback time")
		}
	}
	late, err := client.Get(server.URL + "/api/audio")
	if err != nil {
		t.Fatal(err)
	}
	defer late.Body.Close()
	two := (audioFormat{SampleRate: 48000, Channels: 1}).packet(1520*time.Millisecond, bytes.Repeat([]byte{2, 0}, chunkSamples))
	s.publish(two)
	if string(readPacket(t, late.Body)) != string(two) {
		t.Fatal("late join replayed old audio")
	}
}

func TestSlowRoomDoesNotBlockBroadcast(t *testing.T) {
	s := newStation(time.Second, "test")
	slow, healthy := make(chan []byte, 1), make(chan []byte, 2)
	s.clients[slow], s.clients[healthy] = struct{}{}, struct{}{}
	s.publish([]byte{1})
	s.publish([]byte{2})
	if len(s.clients) != 1 {
		t.Fatal("slow room was not removed")
	}
	if (<-healthy)[0] != 1 || (<-healthy)[0] != 2 {
		t.Fatal("healthy room lost audio")
	}
	<-slow
	if _, ok := <-slow; ok {
		t.Fatal("slow room channel not closed")
	}
}
