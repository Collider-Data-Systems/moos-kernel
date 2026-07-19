package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log"
	"math/big"
	"net"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// DevTLSConfig builds the TLS configuration for the P9 WebTransport SPIKE
// listener (see webtransport.go).
//
// If certFile and keyFile are BOTH non-empty it loads that operator-supplied
// key pair from disk. Otherwise it generates an EPHEMERAL, in-memory,
// self-signed certificate for localhost: the private key is created at startup,
// lives only in this process's memory, and is NEVER written to disk or
// committed to the repo. This is dev-only / localhost-only — the spike is a
// synthetic datagram pipe, not a production surface — so a client is expected
// to dial with InsecureSkipVerify (or pin the ephemeral leaf out-of-band).
//
// The returned config advertises the "h3" ALPN so the QUIC handshake
// negotiates HTTP/3. webtransport-go's Serve path passes this tls.Config
// straight to quic.ListenEarly without injecting ALPN itself, so we must set it
// here.
func DevTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("wt-spike: load tls key pair: %w", err)
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{http3.NextProtoH3},
		}, nil
	}

	cert, err := ephemeralLocalhostCert()
	if err != nil {
		return nil, err
	}
	log.Printf("wt-spike: no --tls-cert/--tls-key provided — generated an EPHEMERAL in-memory self-signed localhost cert (dev-only; private key never touches disk, never committed)")
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{http3.NextProtoH3},
	}, nil
}

// ephemeralLocalhostCert mints a throwaway self-signed ECDSA P-256 certificate
// valid for localhost / 127.0.0.1 / ::1, ~24h lifetime. The private key is
// returned inside the tls.Certificate (in memory only); nothing is serialized
// to disk, so there is no secret to leak into the repo.
func ephemeralLocalhostCert() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("wt-spike: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("wt-spike: serial: %w", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "moos-wt-spike-dev (ephemeral)"},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("wt-spike: create certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("wt-spike: parse certificate: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        leaf,
	}, nil
}
