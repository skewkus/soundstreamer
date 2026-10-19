package main

import (
	"bytes"
	"net"
	"net/http/httptest"
	"testing"
)

func TestSavedPortVersionAndPublicRestrictions(t *testing.T) {
	a := testApplication(t)
	if a.config.ListenPort != 8080 {
		t.Fatal("default listening port")
	}
	a.activePort = 8080
	for _, port := range []int{8099, 65535, 8080} {
		if err := a.action(actionRequest{Action: "listenPort", To: port}); err != nil {
			t.Fatal(err)
		}
		saved, err := loadSettings(a.root)
		if err != nil || saved.ListenPort != port {
			t.Fatal("port setting did not persist")
		}
		if a.activePort != 8080 {
			t.Fatal("saved port unexpectedly changed the running listener")
		}
	}
	for _, port := range []int{0, -1, 65536} {
		if err := a.action(actionRequest{Action: "listenPort", To: port}); err == nil {
			t.Fatal("invalid port accepted")
		}
	}
	request := httptest.NewRequest("POST", "/api/control/action", bytes.NewBufferString(`{"action":"listenPort","to":9000}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.station.handler().ServeHTTP(response, request)
	if response.Code != 403 || a.config.ListenPort != 8080 {
		t.Fatal("public controls changed listening port")
	}
	for _, path := range []string{"/admin", "/control", "/stream"} {
		r := httptest.NewRecorder()
		a.station.handler().ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != 200 || !bytes.Contains(r.Body.Bytes(), []byte("Sound Streamer v"+appVersion)) || bytes.Contains(r.Body.Bytes(), []byte("{{APP_VERSION}}")) {
			t.Fatalf("missing version footer on %s", path)
		}
	}
	r := httptest.NewRecorder()
	a.station.handler().ServeHTTP(r, httptest.NewRequest("GET", "/icon.png", nil))
	if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(r.Body.Bytes(), []byte{137, 80, 78, 71}) {
		t.Fatal("application icon not served")
	}
}
func TestTrayBrowserURLUsesActualListener(t *testing.T) {
	for _, test := range []struct {
		ip       string
		port     int
		expected string
	}{{"0.0.0.0", 8080, "http://127.0.0.1:8080"}, {"::", 9000, "http://127.0.0.1:9000"}, {"127.0.0.1", 8123, "http://127.0.0.1:8123"}, {"::1", 8181, "http://[::1]:8181"}} {
		if got := browserBaseURL(&net.TCPAddr{IP: net.ParseIP(test.ip), Port: test.port}, false); got != test.expected {
			t.Fatalf("%s != %s", got, test.expected)
		}
	}
}
