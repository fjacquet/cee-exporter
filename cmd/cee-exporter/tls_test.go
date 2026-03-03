package main

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"
)

func TestMigrateListenConfig(t *testing.T) {
	cases := []struct {
		name     string
		in       ListenConfig
		wantMode string
	}{
		{
			name:     "already_set_manual",
			in:       ListenConfig{TLSMode: "manual"},
			wantMode: "manual",
		},
		{
			name:     "already_set_acme",
			in:       ListenConfig{TLSMode: "acme", TLS: true, CertFile: "/x"},
			wantMode: "acme",
		},
		{
			name:     "legacy_tls_true_with_cert",
			in:       ListenConfig{TLS: true, CertFile: "/etc/cert.crt"},
			wantMode: "manual",
		},
		{
			name:     "legacy_tls_false_no_cert",
			in:       ListenConfig{TLS: false},
			wantMode: "off",
		},
		{
			name:     "tls_true_no_cert_no_mode",
			in:       ListenConfig{TLS: true, CertFile: ""},
			wantMode: "off",
		},
		{
			name:     "zero_value",
			in:       ListenConfig{},
			wantMode: "off",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.in
			migrateListenConfig(&cfg)
			if cfg.TLSMode != tc.wantMode {
				t.Errorf("TLSMode = %q, want %q", cfg.TLSMode, tc.wantMode)
			}
		})
	}
}

func TestBuildSelfSignedTLS(t *testing.T) {
	cases := []struct {
		name  string
		hosts []string
		want  string // expected DNSName in cert (empty = "localhost")
	}{
		{name: "with_hosts", hosts: []string{"example.com"}, want: "example.com"},
		{name: "empty_hosts_fallback", hosts: []string{}, want: "localhost"},
		{name: "nil_hosts_fallback", hosts: nil, want: "localhost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := buildSelfSignedTLS(tc.hosts)
			if err != nil {
				t.Fatalf("buildSelfSignedTLS(%v) error: %v", tc.hosts, err)
			}
			if cfg == nil {
				t.Fatal("expected non-nil *tls.Config")
			}
			if cfg.MinVersion != tls.VersionTLS12 {
				t.Errorf("MinVersion = %d, want %d", cfg.MinVersion, tls.VersionTLS12)
			}
			if len(cfg.Certificates) != 1 {
				t.Fatalf("Certificates len = %d, want 1", len(cfg.Certificates))
			}
			// Parse leaf cert to check fields
			leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
			if err != nil {
				t.Fatalf("parse certificate: %v", err)
			}
			now := time.Now()
			if leaf.NotAfter.Before(now) {
				t.Errorf("cert NotAfter %v is in the past", leaf.NotAfter)
			}
			if leaf.NotBefore.After(now) {
				t.Errorf("cert NotBefore %v is in the future (too strict)", leaf.NotBefore)
			}
			found := false
			for _, dns := range leaf.DNSNames {
				if dns == tc.want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("DNSNames = %v, want to contain %q", leaf.DNSNames, tc.want)
			}
		})
	}
}
