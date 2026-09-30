package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

func newWorkerMTLSServer(addr string, handler http.Handler, certFile, keyFile, clientCAFile string) (*http.Server, error) {
	if strings.TrimSpace(addr) == "" || strings.TrimSpace(certFile) == "" || strings.TrimSpace(keyFile) == "" || strings.TrimSpace(clientCAFile) == "" {
		return nil, errors.New("WORKS_MTLS_ADDR, WORKS_MTLS_CERT, WORKS_MTLS_KEY, and WORKS_MTLS_CLIENT_CA are all required")
	}
	serverCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	clientCAPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, err
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(clientCAPEM) {
		return nil, errors.New("WORKS_MTLS_CLIENT_CA contains no valid certificates")
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    clientCAs,
			Certificates: []tls.Certificate{serverCert},
		},
	}, nil
}
