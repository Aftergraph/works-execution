package worker

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// NewMTLSHTTPClient builds a worker transport that verifies the WORKS server
// certificate against caFile and presents the provisioned worker certificate.
// serverName is mandatory so clients that dial a private IP or relay still
// verify the intended WORKS service identity instead of the transport address.
func NewMTLSHTTPClient(timeout time.Duration, caFile, certFile, keyFile, serverName string) (*http.Client, error) {
	caFile = strings.TrimSpace(caFile)
	certFile = strings.TrimSpace(certFile)
	keyFile = strings.TrimSpace(keyFile)
	serverName = strings.TrimSpace(serverName)
	if caFile == "" || certFile == "" || keyFile == "" || serverName == "" {
		return nil, errors.New("worker mTLS requires CA, client certificate, client key, and server name")
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read WORKS server CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("WORKS server CA file contains no valid certificates")
	}
	clientCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load worker mTLS certificate: %w", err)
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport is not an *http.Transport")
	}
	mtlsTransport := transport.Clone()
	mtlsTransport.TLSClientConfig = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      roots,
		Certificates: []tls.Certificate{clientCert},
		ServerName:   serverName,
	}
	return &http.Client{Timeout: timeout, Transport: mtlsTransport}, nil
}
