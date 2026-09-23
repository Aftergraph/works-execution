package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func benchWork(i int) *workgraph.Work {
	return &workgraph.Work{
		ID: workgraph.NewID("work"),
		State: workgraph.StateQueued,
		Source: workgraph.Source{
			Type: "git", Repository: "org/repo", SHA: "0123456789abcdef0123456789abcdef01234567",
		},
		Objective: workgraph.Objective{Type: "build", Description: "bench"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"build": {Run: "go build ./..."},
			"test":  {Run: "go test ./..."},
			"lint":  {Run: "go vet ./..."},
		}},
		Requirements: workgraph.Requirements{OS: "linux", Arch: "amd64", CPUMilli: 1000, MemoryMiB: 256},
	}
}

func seedBenchWorks(b *testing.B, s *SQLiteStore, n int) {
	b.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := s.CreateWork(ctx, benchWork(i)); err != nil {
			b.Fatal(err)
		}
	}
}

func openBenchStore(b *testing.B) *SQLiteStore {
	b.Helper()
	s, err := Open(fmt.Sprintf("%s/bench.db", b.TempDir()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func BenchmarkListWorks(b *testing.B) {
	s := openBenchStore(b)
	seedBenchWorks(b, s, 500)
	ctx := context.Background()
	for _, limit := range []int{50, 100, 200} {
		b.Run(fmt.Sprintf("limit=%d", limit), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := s.ListWorks(ctx, limit); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkReadyPath measures the store side of the /v1/workers/ready poll:
// schedulable-list + active-lease resolution for the candidate set.
func BenchmarkReadyPath(b *testing.B) {
	s := openBenchStore(b)
	seedBenchWorks(b, s, 300)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		works, err := s.ListSchedulableWorks(ctx, 100)
		if err != nil {
			b.Fatal(err)
		}
		ids := make([]string, 0, len(works))
		for _, w := range works {
			ids = append(ids, w.ID)
		}
		if _, err := s.ActiveLeasesByWorkIDs(ctx, ids); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListWorksConcurrent measures 10 parallel readers issuing
// ListWorks(50) each — the dashboard/SSE read pattern.
func BenchmarkListWorksConcurrent(b *testing.B) {
	s := openBenchStore(b)
	seedBenchWorks(b, s, 300)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		done := make(chan error, 10)
		for k := 0; k < 10; k++ {
			go func() {
				_, err := s.ListWorks(ctx, 50)
				done <- err
			}()
		}
		for k := 0; k < 10; k++ {
			if err := <-done; err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkAppendWorkEvent(b *testing.B) {
	s := openBenchStore(b)
	seedBenchWorks(b, s, 10)
	ctx := context.Background()
	works, err := s.ListWorks(ctx, 10)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ev, err := s.AppendWorkEvent(ctx, WorkEvent{
			ID:     workgraph.NewID("evt"),
			WorkID: works[0].ID,
			Type:   EventWorkStateChanged,
			Data:   []byte(`{"state":"running"}`),
		})
		if err != nil {
			b.Fatal(err)
		}
		_ = ev
	}
}
