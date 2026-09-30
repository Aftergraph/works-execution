package identity

import (
	"crypto/x509"
	"net/url"
	"testing"
)

func TestWorkerIDURIAndParseRoundTrip(t *testing.T) {
	uri, err := WorkerIDURI("wrkr_jonas_lenovo")
	if err != nil {
		t.Fatal(err)
	}
	const want = "spiffe://aftergraph.org/ns/workers/sa/wrkr_jonas_lenovo"
	if uri != want {
		t.Fatalf("WorkerIDURI() = %q, want %q", uri, want)
	}
	id, err := Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := id.WorkerID(); !ok || got != "wrkr_jonas_lenovo" {
		t.Fatalf("WorkerID() = %q, %v", got, ok)
	}
	if got := id.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestParseRejectsAliasesAndMalformedIdentities(t *testing.T) {
	for _, raw := range []string{
		"https://aftergraph.org/ns/workers/sa/wrkr_jonas_lenovo",
		"spiffe://aftergraph.org/workers/wrkr_jonas_lenovo",
		"spiffe://aftergraph.org/ns/workers/sa/wrkr_jonas_lenovo?x=1",
		"spiffe://aftergraph.org:443/ns/workers/sa/wrkr_jonas_lenovo",
		"spiffe://Aftergraph.org/ns/workers/sa/wrkr_jonas_lenovo",
		"spiffe://aftergraph.org/ns/workers/sa/wrkr%5Fjonas",
		"spiffe://aftergraph.org/ns/workers/sa/WRKR_JONAS_LENOVO",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatalf("Parse(%q) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestWorkerIDURIRejectsInvalidIDs(t *testing.T) {
	for _, workerID := range []string{"", "worker-1", "wrkr_UPPER", "wrkr_"} {
		if _, err := WorkerIDURI(workerID); err == nil {
			t.Fatalf("WorkerIDURI(%q) unexpectedly succeeded", workerID)
		}
	}
}

func TestWorkerIDFromCertificate(t *testing.T) {
	valid, err := url.Parse("spiffe://aftergraph.org/ns/workers/sa/wrkr_jonas_lenovo")
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{URIs: []*url.URL{valid}}
	if got, err := WorkerIDFromCertificate(cert); err != nil || got != "wrkr_jonas_lenovo" {
		t.Fatalf("WorkerIDFromCertificate() = %q, %v", got, err)
	}

	invalid, err := url.Parse("spiffe://aftergraph.org/ns/operators/sa/wrkr_jonas_lenovo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WorkerIDFromCertificate(&x509.Certificate{URIs: []*url.URL{valid, invalid}}); err == nil {
		t.Fatal("WorkerIDFromCertificate accepted a certificate with an extra Aftergraph identity")
	}
	if _, err := WorkerIDFromCertificate(nil); err == nil {
		t.Fatal("WorkerIDFromCertificate accepted a missing certificate")
	}
}
