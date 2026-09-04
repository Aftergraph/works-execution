package cache_test

import (
	"testing"

	"github.com/JonasAbde/works-execution/packages/cache"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Regression for the push-CI cache deadlock: the default fingerprint
// includes the push SHA, so every push produced a fresh key and cache:
// true could never hit on push-triggered works. CacheSpec.KeyInputs
// lets a works.yml exclude SHA (and Ref) from the key.
func TestKeyFromNode_KeyInputsExcludeSHA(t *testing.T) {
	w1 := sampleWork("acme/widgets", "1111111111111111111111111111111111111111")
	w2 := sampleWork("acme/widgets", "2222222222222222222222222222222222222222")
	n := sampleNode("go test ./...")

	// Default (no CacheSpec): SHA change MUST change the key.
	f1, err := cache.KeyFromNode(w1, n, "organization").Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	f2, err := cache.KeyFromNode(w2, n, "organization").Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if f1 == f2 {
		t.Fatal("default key must include SHA (different SHAs must differ)")
	}

	// With KeyInputs [run, repository]: SHA change must NOT change key.
	spec := &workgraph.CacheSpec{Enabled: true, KeyInputs: []string{"run", "repository"}, Scope: "organization"}
	nKeyed := sampleNode("go test ./...")
	nKeyed.CacheSpec = spec

	g1, err := cache.KeyFromNode(w1, nKeyed, "organization").Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	g2, err := cache.KeyFromNode(w2, nKeyed, "organization").Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if g1 != g2 {
		t.Fatalf("key_inputs without sha: SHA change must not change key (%s vs %s)", g1, g2)
	}
	if g1 == f1 {
		t.Fatal("keyed key must differ from default key (fewer inputs = different hash)")
	}

	// Run change still changes the key under key_inputs [run, repository].
	nKeyed2 := sampleNode("go test ./... -race")
	nKeyed2.CacheSpec = spec
	g3, err := cache.KeyFromNode(w1, nKeyed2, "organization").Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if g3 == g1 {
		t.Fatal("run change must change key even under key_inputs")
	}

	// Repository change still changes the key under key_inputs.
	w3 := sampleWork("acme/other", "1111111111111111111111111111111111111111")
	g4, err := cache.KeyFromNode(w3, nKeyed, "organization").Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if g4 == g1 {
		t.Fatal("repository change must change key even under key_inputs")
	}

	// Empty KeyInputs = default behavior (SHA included).
	nEmpty := sampleNode("go test ./...")
	nEmpty.CacheSpec = &workgraph.CacheSpec{Enabled: true, Scope: "organization"}
	h1, err := cache.KeyFromNode(w1, nEmpty, "organization").Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := cache.KeyFromNode(w2, nEmpty, "organization").Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 || h1 != f1 {
		t.Fatal("empty key_inputs must behave exactly like no CacheSpec")
	}
}
