package workspace

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

type staticResolver struct{ values map[string]string }
func (s staticResolver) Resolve(_ context.Context, ref *secrets.Ref, _ string) (string, error) {
	return s.values[ref.String()], nil
}

func TestGitHubAppIssuerMintsRepoScopedToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil { t.Fatal(err) }
	keyPEM := pem.EncodeToMemory(&pem.Block{Type:"RSA PRIVATE KEY",Bytes:x509.MarshalPKCS1PrivateKey(key)})
	var sawRepo, sawWrite, sawJWT, sawRevoke bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens":
			auth := r.Header.Get("Authorization")
			sawJWT = strings.HasPrefix(auth, "Bearer ") && strings.Count(strings.TrimPrefix(auth,"Bearer "), ".") == 2
			var body struct {
				Repositories []string `json:"repositories"`
				Permissions map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sawRepo = len(body.Repositories)==1 && body.Repositories[0]=="runtime"
			sawWrite = body.Permissions["contents"]=="write" && len(body.Permissions)==1
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":"installation-secret-token",
				"expires_at":"2026-10-01T19:00:00Z",
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
			sawRevoke = r.Header.Get("Authorization") == "Bearer installation-secret-token"
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	ref := secrets.Must("secret://github/app-key")
	issuer, err := NewGitHubAppIssuer(GitHubAppIssuerConfig{
		AppID:7, InstallationID:42, PrivateKeyRef:ref, APIBase:ts.URL,
	}, staticResolver{values:map[string]string{ref.String():string(keyPEM)}}, ts.Client())
	if err != nil { t.Fatal(err) }
	issuer.now = func() time.Time { return time.Date(2026,10,1,18,0,0,0,time.UTC) }

	cred, err := issuer.Issue(context.Background(),"Aftergraph/runtime","wrk_123",ModeWrite,30*time.Minute)
	if err != nil { t.Fatal(err) }
	if !sawRepo || !sawWrite || !sawJWT {
		t.Fatalf("scoping missing repo=%v write=%v jwt=%v",sawRepo,sawWrite,sawJWT)
	}
	if cred.Plaintext != "installation-secret-token" {
		t.Fatal("wrong token returned")
	}
	if !strings.HasPrefix(cred.ID,"ghinst_") {
		t.Fatalf("bad correlation id %q",cred.ID)
	}
	want := time.Date(2026,10,1,18,30,0,0,time.UTC)
	if !cred.ExpiresAt.Equal(want) {
		t.Fatalf("logical ttl not attenuated: got %s want %s",cred.ExpiresAt,want)
	}
	if err := issuer.Revoke(context.Background(), cred.ID, cred.Plaintext); err != nil {
		t.Fatal(err)
	}
	if !sawRevoke {
		t.Fatal("installation token was not revoked server-side")
	}
}

func TestGitHubAppIssuerRejectsUnknownRevokeID(t *testing.T) {
	issuer := &GitHubAppIssuer{}
	if err := issuer.Revoke(context.Background(),"raw-token-id","plaintext"); err == nil {
		t.Fatal("unknown token id accepted")
	}
}
