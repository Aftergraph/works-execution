package main

import (
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Dockerfile HEALTHCHECK runs `/works-api -healthz-only`. Before this flag
// existed, flag.Parse rejected it with exit 2 and every container built from the
// image was marked unhealthy while serving correctly.

func TestHealthzOnlyFlagIsRegistered(t *testing.T) {
	// Registered means the Dockerfile's invocation parses. Look the flag up in
	// the global FlagSet that flag.Parse consumed, rather than the pointer.
	registered := flag.Lookup("healthz-only")
	if registered == nil {
		t.Fatal("-healthz-only must be registered as a flag")
	}
	if registered.DefValue != "false" {
		t.Fatalf("-healthz-only default = %q, want \"false\" (a default of true would make the server exit immediately)", registered.DefValue)
	}
	if healthzOnly == nil {
		t.Fatal("the healthzOnly flag handle must be bound")
	}
}

func TestHealthzOnlyExitsZeroAgainstAHealthyListener(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	if code := healthzOnlyExit(srv.Listener.Addr().String()); code != 0 {
		t.Fatalf("healthzOnlyExit against a healthy listener = %d, want 0", code)
	}
}

func TestHealthzOnlyExitsNonZeroAgainstAnUnhealthyListener(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"non-200": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"404 on /healthz": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if code := healthzOnlyExit(srv.Listener.Addr().String()); code == 0 {
				t.Fatal("healthzOnlyExit reported healthy for an unhealthy listener")
			}
		})
	}
}

func TestHealthzOnlyFailsWhenNothingIsListening(t *testing.T) {
	// Bind then immediately release a port so nothing is serving on it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	if code := healthzOnlyExit(addr); code == 0 {
		t.Fatal("healthzOnlyExit reported healthy with nothing listening")
	}
}

func TestHealthzOnlyRejectsAnUnparseableAddr(t *testing.T) {
	if code := healthzOnlyExit("not-a-host-port"); code == 0 {
		t.Fatal("healthzOnlyExit accepted an unparseable -addr")
	}
}

// A wildcard bind is what the Dockerfile configures (WORKS_ADDR=0.0.0.0:8080).
// That address is not dialable, so the probe must retarget loopback instead of
// reporting a false negative.
func TestHealthzOnlyRetargetsWildcardBindToLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split port: %v", err)
	}
	// Build each listen address the way a user would actually pass it to -addr.
	// The IPv6 wildcard keeps its brackets: SplitHostPort rejects "[[::]]:8080".
	for _, addr := range []string{
		net.JoinHostPort("0.0.0.0", port),
		net.JoinHostPort("", port),
		"[::]:" + port,
	} {
		if code := healthzOnlyExit(addr); code != 0 {
			t.Fatalf("healthzOnlyExit(%q) = %d, want 0 — a wildcard bind must be probed on loopback", addr, code)
		}
	}
}
