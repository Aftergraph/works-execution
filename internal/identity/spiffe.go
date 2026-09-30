// Package identity defines the canonical workload identity used by Aftergraph workers.
package identity

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	AftergraphTrustDomain = "aftergraph.org"
	WorkerNamespace       = "workers"
)

var (
	segmentPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	workerIDPattern = regexp.MustCompile(`^wrkr_[a-z0-9_-]{1,64}$`)
)

// SPIFFEID is the canonical identity tuple for a workload.
// Its string form follows spiffe://<trust-domain>/ns/<namespace>/sa/<service-account>.
type SPIFFEID struct {
	TrustDomain    string
	Namespace      string
	ServiceAccount string
}

func (id SPIFFEID) String() string {
	return fmt.Sprintf("spiffe://%s/ns/%s/sa/%s", id.TrustDomain, id.Namespace, id.ServiceAccount)
}

// Parse accepts only the canonical SPIFFE URI shape used by WORKS.
// Percent-escaped segments, ports, queries, fragments, user info, and
// non-canonical casing are rejected so identities cannot have aliases.
func Parse(raw string) (SPIFFEID, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "spiffe" || u.Opaque != "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Port() != "" ||
		u.Host == "" || u.Host != strings.ToLower(u.Host) {
		return SPIFFEID{}, errors.New("identity: malformed SPIFFE URI")
	}
	if strings.Contains(u.Host, ":") || strings.Contains(u.Host, "%") {
		return SPIFFEID{}, errors.New("identity: trust domain must be a canonical DNS name")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "ns" || parts[2] != "sa" {
		return SPIFFEID{}, errors.New("identity: SPIFFE URI must use /ns/<namespace>/sa/<service-account>")
	}
	if !segmentPattern.MatchString(parts[1]) || !segmentPattern.MatchString(parts[3]) {
		return SPIFFEID{}, errors.New("identity: namespace and service account must be canonical path segments")
	}
	id := SPIFFEID{TrustDomain: u.Host, Namespace: parts[1], ServiceAccount: parts[3]}
	if id.String() != raw {
		return SPIFFEID{}, errors.New("identity: SPIFFE URI is not in canonical form")
	}
	return id, nil
}

// WorkerID returns the registered worker id only for Aftergraph worker identities.
func (id SPIFFEID) WorkerID() (string, bool) {
	if id.TrustDomain != AftergraphTrustDomain || id.Namespace != WorkerNamespace || !workerIDPattern.MatchString(id.ServiceAccount) {
		return "", false
	}
	return id.ServiceAccount, true
}

// WorkerIDURI constructs the one canonical SPIFFE URI accepted for a WORKS worker.
func WorkerIDURI(workerID string) (string, error) {
	if !workerIDPattern.MatchString(workerID) {
		return "", errors.New("identity: invalid WORKS worker id")
	}
	return SPIFFEID{TrustDomain: AftergraphTrustDomain, Namespace: WorkerNamespace, ServiceAccount: workerID}.String(), nil
}

// WorkerIDFromCertificate returns the single canonical Aftergraph worker
// identity carried in a client certificate URI SAN. TLS verification must
// already have succeeded against the configured worker CA before this is used.
func WorkerIDFromCertificate(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", errors.New("identity: client certificate is missing")
	}
	var workerID string
	for _, uri := range cert.URIs {
		if uri == nil || uri.Scheme != "spiffe" || uri.Host != AftergraphTrustDomain {
			continue
		}
		parsed, err := Parse(uri.String())
		if err != nil {
			return "", err
		}
		candidate, ok := parsed.WorkerID()
		if !ok {
			return "", errors.New("identity: certificate has a non-worker Aftergraph identity")
		}
		if workerID != "" {
			return "", errors.New("identity: certificate has multiple Aftergraph worker identities")
		}
		workerID = candidate
	}
	if workerID == "" {
		return "", errors.New("identity: certificate has no Aftergraph worker identity")
	}
	return workerID, nil
}
