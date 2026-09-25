package evidence

import (
	"crypto/sha256"
	"testing"

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
