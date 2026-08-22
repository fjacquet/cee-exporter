//go:build !windows

package evtx

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	goevtx "github.com/fjacquet/go-evtx"
)

// BenchmarkEvtxWriteBatch is expected to show NO improvement over the
// per-event path: go-evtx's WriteRecord takes one record and fsyncs per
// chunk. It is here so that stays visible rather than assumed.
//
// BinaryEvtxWriter only exists on !windows (writer_evtx_notwindows.go), so
// this benchmark lives in its own file rather than the untagged
// writer_bench_test.go — never a _linux.go suffix, which Go treats as
// Linux-only.
func BenchmarkEvtxWriteBatch(b *testing.B) {
	for _, size := range []int{1, 100} {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			w, err := NewBinaryEvtxWriter(
				filepath.Join(b.TempDir(), "bench.evtx"), goevtx.RotationConfig{})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = w.Close() })

			batch := benchBatch(size)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := w.WriteBatch(context.Background(), batch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
