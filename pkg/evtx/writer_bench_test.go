package evtx

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
)

// benchSinkTCP accepts connections and discards everything, never closing
// them — the opposite of flakySink, which exists to force reconnects.
func benchSinkTCP(b *testing.B) (host string, port int) {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _, _ = io.Copy(io.Discard, c) }(c)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func benchSinkUDP(b *testing.B) (host string, port int) {
	b.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	addr := pc.LocalAddr().(*net.UDPAddr)
	return addr.IP.String(), addr.Port
}

func benchBatch(size int) []WindowsEvent {
	batch := make([]WindowsEvent, size)
	for i := range batch {
		batch[i] = raceEvent()
	}
	return batch
}

// Read these as ns/op DIVIDED BY the batch size. The spec's table is
// per-event (GELF TCP measured 11.4 µs/event on the per-event path), and
// comparing a batch=500 ns/op against that figure directly will look like a
// 500x regression when it is a ~500x speedup per event.
func BenchmarkGELFWriteBatch(b *testing.B) {
	for _, proto := range []string{"tcp", "udp"} {
		for _, size := range []int{1, 10, 100, 500} {
			b.Run(fmt.Sprintf("%s/batch=%d", proto, size), func(b *testing.B) {
				var host string
				var port int
				if proto == "tcp" {
					host, port = benchSinkTCP(b)
				} else {
					host, port = benchSinkUDP(b)
				}
				w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: proto})
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
}

// BenchmarkGELFWriteEvent is the baseline the batch numbers are measured
// against. Without it there is nothing to compare to and the improvement
// claim rests on a figure recorded in a document.
func BenchmarkGELFWriteEvent(b *testing.B) {
	host, port := benchSinkTCP(b)
	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = w.Close() })

	e := raceEvent()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := w.WriteEvent(context.Background(), e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSyslogWriteBatch(b *testing.B) {
	for _, size := range []int{1, 10, 100, 500} {
		b.Run(fmt.Sprintf("tcp/batch=%d", size), func(b *testing.B) {
			host, port := benchSinkTCP(b)
			w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "tcp"})
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

func BenchmarkSyslogWriteEvent(b *testing.B) {
	host, port := benchSinkTCP(b)
	w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = w.Close() })

	e := raceEvent()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := w.WriteEvent(context.Background(), e); err != nil {
			b.Fatal(err)
		}
	}
}
