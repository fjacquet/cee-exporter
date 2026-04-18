// tls.go — TLS builder functions for cee-exporter
//
// Three TLS modes are supported:
//   - manual:      operator-supplied cert/key files (buildManualTLS)
//   - self-signed: runtime-generated ECDSA certificate (buildSelfSignedTLS)
//   - acme:        Let's Encrypt via autocert (buildAutocertTLS)
//
// No build tag — this file compiles on all platforms (linux, windows) with
// CGO_ENABLED=0. The autocert package is pure Go and has no C dependencies.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// buildManualTLS loads a TLS configuration from operator-supplied cert and key
// files. This is equivalent to the previous buildTLS() function in main.go.
// Plan 02 will rename the call site from buildTLS to buildManualTLS.
func buildManualTLS(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// buildSelfSignedTLS generates a runtime ECDSA certificate signed by itself.
// No files are written to disk — the certificate exists only in memory.
//
// hosts is the list of DNS names to embed in the certificate SAN extension.
// If hosts is empty, []string{"localhost"} is used as a fallback.
func buildSelfSignedTLS(hosts []string) (*tls.Config, error) {
	if len(hosts) == 0 {
		hosts = []string{"localhost"}
	}

	// Generate ECDSA P-256 key pair.
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ECDSA key: %w", err)
	}

	// Marshal private key to PKCS8 DER bytes.
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("marshal EC private key: %w", err)
	}

	// Build certificate template.
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"cee-exporter"},
		},
		DNSNames:  hosts,
		NotBefore: time.Now().Add(-time.Minute), // clock skew tolerance
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}

	// Self-sign the certificate (issuer == subject).
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	// Encode to PEM blocks.
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Load the PEM pair into a tls.Certificate.
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("assemble X509 key pair: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// buildAutocertTLS configures Let's Encrypt ACME certificate management using
// golang.org/x/crypto/acme/autocert. Certificates are obtained automatically
// on first connection and cached in cacheDir.
//
// domains must not be empty — at least one domain is required for ACME
// validation. email is the contact address sent to Let's Encrypt. cacheDir
// defaults to "/var/cache/cee-exporter/acme" when empty.
//
// Returns the autocert.Manager (for use with startACMEChallengeListener) and
// the TLS configuration derived from it.
func buildAutocertTLS(domains []string, email, cacheDir string) (*autocert.Manager, *tls.Config, error) {
	if len(domains) == 0 {
		return nil, nil, fmt.Errorf("acme_domains must not be empty")
	}
	if cacheDir == "" {
		cacheDir = "/var/cache/cee-exporter/acme"
	}

	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Email:      email,
		HostPolicy: autocert.HostWhitelist(domains...),
		Cache:      autocert.DirCache(cacheDir),
	}

	return m, m.TLSConfig(), nil
}

// startACMEChallengeListener starts an HTTP-01 ACME challenge listener on
// challengeAddr (default ":443"). The listener is started in a goroutine;
// errors from ServeTLS are logged but not fatal — the ACME renewal loop
// continues even if individual challenge responses fail.
//
// challengeAddr must be reachable from the Let's Encrypt challenge servers on
// port 443. This usually requires port forwarding or a privileged listener.
func startACMEChallengeListener(m *autocert.Manager, challengeAddr string) error {
	if challengeAddr == "" {
		challengeAddr = ":443"
	}

	ln, err := net.Listen("tcp", challengeAddr)
	if err != nil {
		return fmt.Errorf("acme challenge listener: %w", err)
	}

	go func() {
		srv := &http.Server{TLSConfig: m.TLSConfig()}
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("acme_challenge_listener_error", "addr", challengeAddr, "error", err)
		}
	}()

	slog.Info("acme_challenge_listener_started", "addr", challengeAddr)
	return nil
}

// logCertInfo logs a startup message for the loaded TLS certificate.
// This function will be the sole definition once Plan 02 removes the
// duplicate from main.go.
func logCertInfo(certFile string) {
	slog.Info("tls_cert_loaded", "cert_file", certFile)
}
