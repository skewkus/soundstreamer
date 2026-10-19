package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashAndLegacyMigration(t *testing.T) {
	if hex.EncodeToString(passwordKey("password", []byte("salt"), 2)) != "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43" {
		t.Fatal("PBKDF2 does not match independent test vector")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "soundstreamer.json"), []byte(`{"version":1,"adminPassword":"old-password","musicFolder":"music","controlFrozen":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := newApplication(ctx, root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword("old-password", a.config.AdminPasswordHash) || verifyPassword("wrong-password", a.config.AdminPasswordHash) {
		t.Fatal("legacy password did not migrate")
	}
	data, err := os.ReadFile(filepath.Join(root, "soundstreamer.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"adminPassword"`)) || bytes.Contains(data, []byte("old-password")) {
		t.Fatal("plaintext password remains on disk")
	}
	if !a.config.ControlFrozen || len(a.config.MusicFolders) != 1 || a.config.MusicFolders[0] != "music" {
		t.Fatal("migration lost unrelated settings")
	}
}

func TestControlFreezePasswordChangesAndArtistSorting(t *testing.T) {
	a := testApplication(t)
	h := a.station.handler()
	call := func(method, path string, payload any, cookie *http.Cookie) *httptest.ResponseRecorder {
		var b []byte
		if payload != nil {
			b, _ = json.Marshal(payload)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if !strings.Contains(call("GET", "/api/admin/auth", nil, nil).Body.String(), `"setupRequired":true`) {
		t.Fatal("missing first-run setup")
	}
	setup := call("POST", "/api/admin/setup", map[string]string{"password": "first-password"}, nil)
	if setup.Code != 200 {
		t.Fatalf("setup failed: %s", setup.Body.String())
	}
	cookie := setup.Result().Cookies()[0]
	if call("POST", "/api/admin/setup", map[string]string{"password": "takeover-password"}, nil).Code == 200 {
		t.Fatal("setup overwrote existing password")
	}
	if call("GET", "/api/control/state", nil, nil).Code != 200 {
		t.Fatal("control requires a password")
	}
	if call("POST", "/api/control/action", actionRequest{Action: "shuffle", Enabled: true}, nil).Code != 200 {
		t.Fatal("public playback control unavailable")
	}
	for _, action := range []string{"freeze", "source", "resume", "rescan"} {
		if call("POST", "/api/control/action", actionRequest{Action: action}, nil).Code != 403 {
			t.Fatalf("public settings action %s accepted", action)
		}
	}
	if call("POST", "/api/admin/action", actionRequest{Action: "freeze", Enabled: true}, cookie).Code != 200 {
		t.Fatal("admin freeze failed")
	}
	for _, adminCookie := range []*http.Cookie{nil, cookie} {
		if call("POST", "/api/control/action", actionRequest{Action: "shuffle", Enabled: false}, adminCookie).Code != 423 {
			t.Fatal("frozen control bypassed using a direct request or admin cookie")
		}
	}
	if !a.config.Shuffle {
		t.Fatal("frozen request changed state")
	}
	if call("POST", "/api/admin/action", actionRequest{Action: "shuffle", Enabled: false}, cookie).Code != 200 || a.config.Shuffle {
		t.Fatal("freeze disabled admin")
	}
	if call("POST", "/api/admin/action", actionRequest{Action: "freeze", Enabled: false}, cookie).Code != 200 {
		t.Fatal("unfreeze failed")
	}
	if call("POST", "/api/control/action", actionRequest{Action: "shuffle", Enabled: true}, nil).Code != 200 {
		t.Fatal("controls did not recover")
	}
	a.library = map[string]media{"a": {ID: "a", Title: "Zebra", Artist: "Alpha", Kind: "audio"}, "b": {ID: "b", Title: "Apple", Artist: "Zulu", Kind: "audio"}, "c": {ID: "c", Title: "Apple", Artist: "Alpha", Kind: "audio"}}
	list := call("GET", "/api/control/library", nil, nil)
	var library struct{ Items []media }
	if err := json.Unmarshal(list.Body.Bytes(), &library); err != nil {
		t.Fatal(err)
	}
	if len(library.Items) != 3 || library.Items[0].ID != "c" || library.Items[1].ID != "a" || library.Items[2].ID != "b" {
		t.Fatal("library is not sorted by artist then title")
	}
	a.config.Current = &library.Items[0]
	if a.clock()["artist"] != "Alpha" || a.clock()["title"] != "Apple" {
		t.Fatal("stream metadata lost artist/title")
	}
	changed := call("POST", "/api/admin/password", map[string]string{"currentPassword": "first-password", "newPassword": "second-password"}, cookie)
	if changed.Code != 200 {
		t.Fatalf("password change: %s", changed.Body.String())
	}
	if call("GET", "/api/admin/state", nil, cookie).Code != 401 {
		t.Fatal("old admin session survived password change")
	}
	if call("GET", "/api/admin/state", nil, changed.Result().Cookies()[0]).Code != 200 {
		t.Fatal("password change signed out current admin")
	}
	if call("POST", "/api/admin/login", map[string]string{"password": "first-password"}, nil).Code != 401 {
		t.Fatal("old password still accepted")
	}
	if call("POST", "/api/admin/login", map[string]string{"password": "second-password"}, nil).Code != 200 {
		t.Fatal("new password rejected")
	}
	for _, path := range []string{"/stream", "/control", "/admin"} {
		if call("GET", path, nil, nil).Code != 200 {
			t.Fatalf("%s unavailable", path)
		}
	}
	if !strings.Contains(call("GET", "/control", nil, nil).Body.String(), `<body class="control-mode">`) {
		t.Fatal("control settings not hidden before JavaScript loads")
	}
	for _, path := range []string{"/listen", "/watch"} {
		r := call("GET", path, nil, nil)
		if r.Header().Get("Location") != "/stream" {
			t.Fatal("old portal did not redirect")
		}
	}
}
