package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/worker"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func requestWithVerifiedWorkerCertificate(method, path, body, workerID string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	uri, _ := url.Parse("spiffe://aftergraph.org/ns/workers/sa/" + workerID)
	cert := &x509.Certificate{URIs: []*url.URL{uri}}
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	return req
}

func TestWorkerMTLSEnrollmentBindsWorkerIDToCertificate(t *testing.T) {
	srv := &Server{AuthEnabled: true, RequireWorkerMTLS: true, Auth: NewHMACIssuer()}
	handler := srv.Routes()
	body := `{"worker_id":"wrkr_jonas_lenovo","challenge":"","ttl_seconds":60}`
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, requestWithVerifiedWorkerCertificate(http.MethodPost, "/v1/workers/enroll", body, "wrkr_jonas_lenovo"))
	if resp.Code != http.StatusOK {
		t.Fatalf("matching mTLS enrollment status = %d body=%s, want 200", resp.Code, resp.Body.String())
	}
	var enrollment enrollmentResp
	if err := json.Unmarshal(resp.Body.Bytes(), &enrollment); err != nil {
		t.Fatal(err)
	}
	if enrollment.Token == "" || enrollment.WorkerID != "wrkr_jonas_lenovo" {
		t.Fatalf("mTLS enrollment response = %#v, want a token bound to wrkr_jonas_lenovo", enrollment)
	}

	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, requestWithVerifiedWorkerCertificate(http.MethodPost, "/v1/workers/enroll", body, "wrkr_other_node"))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("mismatched mTLS enrollment status = %d body=%s, want 403", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/workers/enroll", strings.NewReader(body)))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("enrollment without mTLS status = %d body=%s, want 401", resp.Code, resp.Body.String())
	}
}

func TestWorkerMTLSRejectsBearerCertificateIdentityMismatch(t *testing.T) {
	auth := NewHMACIssuer()
	token, err := auth.Mint(t.Context(), "wrkr_jonas_lenovo", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{AuthEnabled: true, RequireWorkerMTLS: true, Auth: auth}
	resp := httptest.NewRecorder()
	req := requestWithVerifiedWorkerCertificate(http.MethodGet, "/v1/workers/ready", "", "wrkr_other_node")
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Routes().ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("certificate/token mismatch status = %d body=%s, want 403", resp.Code, resp.Body.String())
	}
}

func TestWorkerMTLSHTTPSEnrollmentAndReadyPoll(t *testing.T) {
	caCert, caKey, caPEM := newTestWorkerCA(t)
	serverCert := newTestWorkerLeaf(t, caCert, caKey, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		DNSNames:     []string{"works-mtls.aftergraph.org"},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	workerURI, err := url.Parse("spiffe://aftergraph.org/ns/workers/sa/wrkr_jonas_lenovo")
	if err != nil {
		t.Fatal(err)
	}
	clientCert := newTestWorkerLeaf(t, caCert, caKey, &x509.Certificate{
		SerialNumber: big.NewInt(3),
		URIs:         []*url.URL{workerURI},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})

	caFile := filepath.Join(t.TempDir(), "worker-test-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	clientCertFile, clientKeyFile := writeTestPair(t, "worker-client", clientCert)
	transport, err := worker.NewMTLSHTTPClient(5*time.Second, caFile, clientCertFile, clientKeyFile, "works-mtls.aftergraph.org")
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "worker-mtls.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := &Server{Store: st, AuthEnabled: true, RequireWorkerMTLS: true, Auth: NewHMACIssuer()}
	ts := httptest.NewUnstartedServer(srv.Routes())
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    x509.NewCertPool(),
		Certificates: []tls.Certificate{serverCert},
	}
	tlsConfig.ClientCAs.AddCert(caCert)
	ts.TLS = tlsConfig
	ts.StartTLS()
	defer ts.Close()

	cli := &worker.Client{
		BaseURL:               ts.URL,
		HTTP:                  transport,
		WorkerID:              "wrkr_jonas_lenovo",
		CertificateEnrollment: true,
		EnrollTTL:             time.Minute,
	}
	token, err := cli.Enroll(context.Background(), cli.WorkerID, "", time.Minute)
	if err != nil {
		t.Fatalf("mTLS enrollment failed: %v", err)
	}
	cli.Token = token
	items, err := cli.Ready(context.Background())
	if err != nil {
		t.Fatalf("mTLS-authenticated ready poll failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("ready items = %d, want empty test queue", len(items))
	}
}

func newTestWorkerCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, privateKey, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newTestWorkerLeaf(t *testing.T, caCert *x509.Certificate, caKey ed25519.PrivateKey, template *x509.Certificate) tls.Certificate {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template.NotBefore = now.Add(-time.Minute)
	template.NotAfter = now.Add(time.Hour)
	template.KeyUsage = x509.KeyUsageDigitalSignature
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, publicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return parseTestPair(t, der, privateKey)
}

func parseTestPair(t *testing.T, certDER []byte, key ed25519.PrivateKey) tls.Certificate {
	t.Helper()
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func writeTestPair(t *testing.T, name string, pair tls.Certificate) (string, string) {
	t.Helper()
	certFile := filepath.Join(t.TempDir(), name+".pem")
	keyFile := filepath.Join(t.TempDir(), name+"-key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
