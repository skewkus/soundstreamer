package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testTLSFiles(t *testing.T, root string) *x509.CertPool {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(root, "test.crt"), certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "test.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return roots
}

func TestHTTPSSettingsValidationPersistenceAndAccess(t *testing.T) {
	a := testApplication(t)
	testTLSFiles(t, a.root)
	req := actionRequest{Action: "serverSettings", To: 8443, Enabled: true, HTTPSHostname: "localhost", CertFile: "test.crt", KeyFile: "test.key"}
	if err := a.action(req); err != nil {
		t.Fatal(err)
	}
	saved, err := loadSettings(a.root)
	if err != nil || !saved.HTTPSEnabled || saved.ListenPort != 8443 || saved.CertFile != "test.crt" || saved.KeyFile != "test.key" || saved.HTTPSHostname != "localhost" {
		t.Fatalf("TLS settings not saved: %v", err)
	}
	if a.activeHTTPS {
		t.Fatal("saving enabled TLS before restart")
	}
	for _, invalid := range []actionRequest{
		{Action: "serverSettings", To: 0, Enabled: true, CertFile: "test.crt", KeyFile: "test.key"},
		{Action: "serverSettings", To: 9000, Enabled: true, KeyFile: "test.key"},
		{Action: "serverSettings", To: 9000, Enabled: true, CertFile: "missing.crt", KeyFile: "test.key"},
		{Action: "serverSettings", To: 9000, Enabled: true, CertFile: "test.crt", KeyFile: "test.crt"},
	} {
		if a.action(invalid) == nil {
			t.Fatal("accepted invalid TLS settings")
		}
		if a.config.ListenPort != 8443 || a.config.CertFile != "test.crt" {
			t.Fatal("invalid save partially changed configuration")
		}
	}
	other := t.TempDir()
	testTLSFiles(t, other)
	mismatch := req
	mismatch.KeyFile = filepath.Join(other, "test.key")
	if a.action(mismatch) == nil {
		t.Fatal("accepted mismatched key")
	}
	requestBody, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/control/action", bytes.NewReader(requestBody))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.station.handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("public controls changed TLS")
	}
	w = httptest.NewRecorder()
	a.station.handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/control/state", nil))
	var state map[string]any
	json.Unmarshal(w.Body.Bytes(), &state)
	if _, ok := state["certFile"]; ok {
		t.Fatal("public state exposes certificate path")
	}
	if _, ok := state["keyFile"]; ok {
		t.Fatal("public state exposes private key path")
	}
	// Disabling HTTPS must work even when its files have been moved or deleted.
	if err := a.action(actionRequest{Action: "serverSettings", To: 8080, Enabled: false, CertFile: "missing", KeyFile: "missing"}); err != nil {
		t.Fatal(err)
	}
	if cfg, err := serverTLSConfig(a.root, a.config); err != nil || cfg != nil {
		t.Fatal("HTTP default disabled")
	}
}

func TestHTTPSListenerRoutesCookiesAndStreaming(t *testing.T) {
	a := testApplication(t)
	roots := testTLSFiles(t, a.root)
	a.config.HTTPSEnabled = true
	a.config.CertFile = "test.crt"
	a.config.KeyFile = "test.key"
	cfg, err := serverTLSConfig(a.root, a.config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatal("unexpected minimum TLS version")
	}
	a.activeHTTPS = true
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: a.station.handler(), TLSConfig: cfg}
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	defer func() { server.Close(); <-done }()
	base := browserBaseURL(listener.Addr(), true)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	for _, path := range []string{"/admin", "/control", "/stream", "/api/clock", "/icon.png"} {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.TLS == nil {
			t.Fatalf("HTTPS failed for %s", path)
		}
	}
	resp, err := client.Post(base+"/api/admin/setup", "application/json", strings.NewReader(`{"password":"tls-test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	cookies := resp.Cookies()
	resp.Body.Close()
	if resp.StatusCode != 200 || len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Name != "ss_admin_https" {
		t.Fatal("HTTPS admin cookie is not secure")
	}
	authReq, _ := http.NewRequest("GET", base+"/api/admin/state", nil)
	authReq.AddCookie(cookies[0])
	resp, err = client.Do(authReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("HTTPS admin login failed")
	}
	// Exercise the actual streaming route through TLS, including incremental flush.
	a.background()
	resp, err = client.Get(base + "/api/audio")
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 12+a.audio.chunkBytes())
	_, err = io.ReadFull(resp.Body, data)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("HTTPS audio stream failed: %v", err)
	}
	// A trusted root still must not allow TLS 1.1.
	raw, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11})
	if raw != nil {
		raw.Close()
	}
	if err == nil {
		t.Fatal("obsolete TLS accepted")
	}
}

func TestHTTPSHostnameValidationAndTrayURLs(t *testing.T) {
	a := testApplication(t)
	testTLSFiles(t, a.root)
	c := a.config
	c.HTTPSEnabled, c.CertFile, c.KeyFile, c.HTTPSHostname = true, "test.crt", "test.key", "localhost"
	if _, err := serverTLSConfig(a.root, c); err != nil {
		t.Fatal(err)
	}
	c.HTTPSHostname = "wrong.example.com"
	if _, err := serverTLSConfig(a.root, c); err == nil {
		t.Fatal("accepted hostname not covered by certificate")
	}
	for _, host := range []string{"https://localhost", "localhost:8443", "localhost/admin", "user@localhost", "a..example", "-bad.example", "bad_.example", "bad.example?x=1", "bad.example#fragment"} {
		c.HTTPSHostname = host
		if _, err := serverTLSConfig(a.root, c); err == nil {
			t.Fatalf("accepted invalid hostname %s", host)
		}
	}
	for _, test := range []struct {
		host   string
		secure bool
		port   int
		want   string
	}{
		{"music.example.com", true, 443, "https://music.example.com:443"},
		{" Music.Example.com ", true, 8443, "https://music.example.com:8443"},
		{"localhost", true, 8123, "https://localhost:8123"},
		{"", true, 8443, "https://127.0.0.1:8443"},
		{"music.example.com", false, 8080, "http://127.0.0.1:8080"},
		{"::1", true, 8443, "https://[::1]:8443"},
	} {
		c.HTTPSEnabled, c.HTTPSHostname = test.secure, test.host
		got := configuredBrowserURL(&net.TCPAddr{IP: net.IPv4zero, Port: test.port}, c)
		if got != test.want {
			t.Fatalf("tray URL %q, want %q", got, test.want)
		}
	}
	req := actionRequest{Action: "serverSettings", To: 8443, Enabled: true, HTTPSHostname: "wrong.example.com", CertFile: "test.crt", KeyFile: "test.key"}
	if a.action(req) == nil || a.config.HTTPSEnabled || a.config.HTTPSHostname != "" || a.config.ListenPort != 8080 {
		t.Fatal("invalid hostname partially changed saved configuration")
	}
}
