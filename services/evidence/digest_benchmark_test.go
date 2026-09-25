package evidence

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
	"strings"

	"github.com/zeebo/blake3"
)

var benchmarkDigestSink [32]byte
var benchmarkSetSink DigestSet

func benchmarkPayload(size int) []byte {
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte((i*31 + 17) & 0xff)
	}
	return buf
}

func BenchmarkDigestSHA256_1KiB(b *testing.B) {
	data := benchmarkPayload(1 << 10)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkDigestSink = sha256.Sum256(data)
	}
}

func BenchmarkDigestBLAKE3_1KiB(b *testing.B) {
	data := benchmarkPayload(1 << 10)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkDigestSink = blake3.Sum256(data)
	}
}

func BenchmarkDigestSet_1KiB(b *testing.B) {
	data := benchmarkPayload(1 << 10)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set, err := BuildDigestSet(data, DigestScopeRawBytes)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkSetSink = set
	}
}

func BenchmarkDigestSHA256_1MiB(b *testing.B) {
	data := benchmarkPayload(1 << 20)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkDigestSink = sha256.Sum256(data)
	}
}

func BenchmarkDigestBLAKE3_1MiB(b *testing.B) {
	data := benchmarkPayload(1 << 20)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkDigestSink = blake3.Sum256(data)
	}
}

func BenchmarkDigestSet_1MiB(b *testing.B) {
	data := benchmarkPayload(1 << 20)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set, err := BuildDigestSet(data, DigestScopeRawBytes)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkSetSink = set
	}
}

func BenchmarkDigestSHA256_16MiB(b *testing.B) {
	data := benchmarkPayload(16 << 20)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkDigestSink = sha256.Sum256(data)
	}
}

func BenchmarkDigestBLAKE3_16MiB(b *testing.B) {
	data := benchmarkPayload(16 << 20)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkDigestSink = blake3.Sum256(data)
	}
}

func BenchmarkDigestSet_16MiB(b *testing.B) {
	data := benchmarkPayload(16 << 20)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set, err := BuildDigestSet(data, DigestScopeRawBytes)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkSetSink = set
	}
}


var benchmarkBundleID string
var benchmarkMAC []byte

func representativeBenchmarkBundle(payloadBytes int) *Bundle {
	payload := strings.Repeat("x", payloadBytes)
	return &Bundle{
		BundleID:  placeholderBundleID,
		WorkID:    "wrk_11111111111111111111111111111111",
		CreatedAt: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
		Runner:    &Runner{ID: "runner-aftergraph", TrustClass: "standard"},
		Summary:   Summary{Result: "SUCCEEDED", DurationMS: 1234},
		Components: Components{
			Attempts: []Attempt{{
				ID: "attempt-1", NodeID: "node-1", WorkerID: "worker-1",
				Status: "succeeded", ExitCode: 0, LeaseID: "lease-1",
			}},
			Artifacts: []ArtifactRef{{
				ID: strings.Repeat("a", 64),
				Digest: "sha256:" + strings.Repeat("a", 64),
				Size: int64(payloadBytes), MimeType: "application/octet-stream",
				NodeID: "node-1", Path: "/artifact",
			}},
			Evidence: []EvidenceRef{{
				ID: "evidence-1", NodeID: "node-1", AttemptID: "attempt-1",
				Type: "test", Result: "pass",
				Details: map[string]any{"payload": payload},
			}},
		},
	}
}

func BenchmarkIntegrityPipelineLegacy_64KiB(b *testing.B) {
	base := representativeBenchmarkBundle(64 << 10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		projection := *base
		projection.BundleID = placeholderBundleID
		projection.Signatures = nil
		projection.Integrity = nil
		canonical, err := canonicalize(&projection)
		if err != nil {
			b.Fatal(err)
		}
		sum := sha256.Sum256(canonical)
		benchmarkBundleID = "evb_" + hex.EncodeToString(sum[:])[:32]
		mac := hmac.New(sha256.New, []byte("benchmark-key"))
		_, _ = mac.Write(canonical)
		benchmarkMAC = mac.Sum(nil)
	}
}

func BenchmarkIntegrityPipelineV1_64KiB(b *testing.B) {
	base := representativeBenchmarkBundle(64 << 10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		projection := *base
		subject, err := bundleSubjectCanonical(&projection)
		if err != nil {
			b.Fatal(err)
		}
		digests, err := BuildDigestSet(subject, DigestScopeCanonicalObject)
		if err != nil {
			b.Fatal(err)
		}
		projection.BundleID = "evb_" + digests.Primary.Value[:32]
		projection.Integrity = &IntegrityEnvelope{
			Canonicalization: BundleCanonicalizationV1,
			SubjectScope: DigestScopeCanonicalObject,
			Digests: digests,
		}
		benchmarkMAC, err = signatureMACFromSubject(&projection, subject, []byte("benchmark-key"))
		if err != nil {
			b.Fatal(err)
		}
		benchmarkBundleID = projection.BundleID
	}
}
