// Package grpctls builds the fleet's gRPC transport credentials. Fleet
// certificates are self-signed, so a dialer verifies the server only when a CA
// bundle is configured and otherwise skips validation. A server enforces mTLS
// when a client CA is configured.
package grpctls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc/credentials"
)

// Files is the certificate material a service uses for one gRPC role.
type Files struct {
	// CAFile verifies the peer. Empty skips validation.
	CAFile string
	// CertFile and KeyFile identify this service to a peer enforcing mTLS.
	CertFile string
	KeyFile  string
	// ServerName overrides the name verified against the peer certificate.
	ServerName string
}

// FromEnv reads client certificate material from GRPC_CA_FILE,
// GRPC_CLIENT_CERT_FILE and GRPC_CLIENT_KEY_FILE. It is the fallback for
// shared clients whose service config does not carry the paths.
func FromEnv() Files {
	return Files{
		CAFile:   strings.TrimSpace(os.Getenv("GRPC_CA_FILE")),
		CertFile: strings.TrimSpace(os.Getenv("GRPC_CLIENT_CERT_FILE")),
		KeyFile:  strings.TrimSpace(os.Getenv("GRPC_CLIENT_KEY_FILE")),
	}
}

func Client(files Files) (credentials.TransportCredentials, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: strings.TrimSpace(files.ServerName)}
	pool, err := loadCA(files.CAFile)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		config.InsecureSkipVerify = true
	} else {
		config.RootCAs = pool
	}

	certFile := strings.TrimSpace(files.CertFile)
	keyFile := strings.TrimSpace(files.KeyFile)
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, errors.New("grpc client TLS requires both certFile and keyFile")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load grpc client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}
	return credentials.NewTLS(config), nil
}

// Server builds inbound credentials. When clientCAFile is set, every client
// must present a certificate signed by that CA.
func Server(certFile, keyFile, clientCAFile string) (credentials.TransportCredentials, error) {
	if strings.TrimSpace(certFile) == "" || strings.TrimSpace(keyFile) == "" {
		return nil, errors.New("grpc server TLS requires certFile and keyFile")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load grpc server certificate: %w", err)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	pool, err := loadCA(clientCAFile)
	if err != nil {
		return nil, err
	}
	if pool != nil {
		config.ClientCAs = pool
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(config), nil
}

func loadCA(path string) (*x509.CertPool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ca file %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("ca file %s contains no certificates", path)
	}
	return pool, nil
}
