package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
)

func TestCaptureRequiresAdmin(t *testing.T) {
	a := testApplication(t)
	if err := a.changePassword("", "capture-test-password", true); err != nil {
		t.Fatal(err)
	}
	invoked := false
	a.pcAudioSource = func(context.Context) (*exec.Cmd, error) {
		invoked = true
		return nil, fmt.Errorf("test capture source")
	}
	session, generation := a.session, a.generation
	for _, action := range []string{"pcAudio", "pcScreen"} {
		payload := []byte(`{"action":"` + action + `"}`)
		for _, authenticated := range []bool{false, true} {
			req := httptest.NewRequest("POST", "/api/control/action", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			if authenticated {
				req.AddCookie(&http.Cookie{Name: "ss_admin", Value: a.adminCookie()})
			}
			w := httptest.NewRecorder()
			a.station.handler().ServeHTTP(w, req)
			if w.Code != 403 {
				t.Fatalf("public %s returned %d", action, w.Code)
			}
		}
		if err := a.dispatchAction(actionRequest{Action: action}, true); err == nil {
			t.Fatalf("public dispatcher accepted %s", action)
		}
		req := httptest.NewRequest("POST", "/api/admin/action", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.station.handler().ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("unauthenticated admin %s returned %d", action, w.Code)
		}
	}
	if invoked || a.pcAudio || a.pcScreen != nil || a.session != session || a.generation != generation {
		t.Fatal("denied request changed playback or started capture")
	}
	// Authenticated admin still reaches capture; the fake source fails before a process starts.
	req := httptest.NewRequest("POST", "/api/admin/action", bytes.NewBufferString(`{"action":"pcAudio"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "ss_admin", Value: a.adminCookie()})
	w := httptest.NewRecorder()
	a.station.handler().ServeHTTP(w, req)
	if !invoked || w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte("test capture source")) {
		t.Fatal("authenticated admin capture unavailable")
	}
	for _, path := range []string{"/control", "/admin"} {
		w := httptest.NewRecorder()
		a.station.handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		for _, id := range []string{`id="pc-audio"`, `id="pc-screen"`, `id="pc-audio-hint"`} {
			if bytes.Contains(w.Body.Bytes(), []byte(id)) != (path == "/admin") {
				t.Fatalf("capture UI incorrect on %s", path)
			}
		}
	}
	req = httptest.NewRequest("POST", "/api/control/action", bytes.NewBufferString(`{"action":"stop"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	a.station.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("normal public playback controls blocked")
	}
}
