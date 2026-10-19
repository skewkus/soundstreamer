package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// Relative certificate paths are resolved beside the configuration, not against
// the working directory of a browser, shortcut or shell.
func serverTLSConfig(root string, c settings) (*tls.Config, error) {
	if !c.HTTPSEnabled {
		return nil, nil
	}
	hostname, err := normalizeHTTPSHostname(c.HTTPSHostname)
	if err != nil {
		return nil, err
	}
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, fmt.Errorf("HTTPS requires both a certificate file and a private key file")
	}
	resolve := func(path string) string {
		if filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(root, path)
	}
	pair, err := tls.LoadX509KeyPair(resolve(c.CertFile), resolve(c.KeyFile))
	if err != nil {
		return nil, fmt.Errorf("could not load HTTPS certificate/key: %w", err)
	}
	if hostname != "" {
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("invalid HTTPS certificate: %w", err)
		}
		if err := leaf.VerifyHostname(hostname); err != nil {
			return nil, fmt.Errorf("HTTPS hostname does not match the certificate: %w", err)
		}
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, nil
}

// Accept a hostname only; the listener supplies the scheme and actual port.
func normalizeHTTPSHostname(value string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(value))
	if host == "" {
		return "", nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	valid := len(host) <= 253
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			valid = false
			break
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				valid = false
				break
			}
		}
	}
	if !valid {
		return "", fmt.Errorf("enter an HTTPS hostname only, for example streamer.example.com, without https://, a port or a path")
	}
	return host, nil
}

func configuredBrowserURL(address net.Addr, c settings) string {
	base := browserBaseURL(address, c.HTTPSEnabled)
	if !c.HTTPSEnabled {
		return base
	}
	host, err := normalizeHTTPSHostname(c.HTTPSHostname)
	if err != nil || host == "" {
		return base
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	parsed.Host = net.JoinHostPort(host, parsed.Port())
	return parsed.String()
}
