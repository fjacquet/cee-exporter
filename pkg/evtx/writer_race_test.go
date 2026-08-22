package evtx

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// raceEvent is the payload used by every writer race test.
func raceEvent() WindowsEvent {
	return WindowsEvent{
		EventID:       4663,
		ProviderName:  DefaultProviderName,
		Computer:      "NAS01",
		Channel:       DefaultChannel,
		TimeCreated:   time.Now().UTC(),
		ObjectType:    "File",
		ObjectName:    `\\NAS01\fs01\dir7\race.txt`,
		AccessMask:    "0x100106",
		CEPAEventType: "CEPP_FILE_WRITE",
	}
}

// flakySink accepts TCP connections and closes each one after a few reads,
// which forces the writer through connect() while other goroutines are inside
// send(). That interleaving — a write to w.conn racing a read of w.conn — is
// the only thing the mutex actually protects. A test that merely writes
// concurrently passes with the mutex deleted, because net.Conn is itself safe
// for concurrent use.
func flakySink(t *testing.T, readsBeforeClose int) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				buf := make([]byte, 4096)
				for i := 0; i < readsBeforeClose; i++ {
					if _, err := c.Read(buf); err != nil {
						break
					}
				}
				_ = c.Close()
			}(c)
		}
	}()
	return ln.Addr().String(), func() {
		close(done)
		_ = ln.Close()
		wg.Wait()
	}
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

// hammer runs write concurrently from many goroutines. Errors are tolerated —
// the sink is deliberately breaking connections — because the assertion under
// test is the race detector, not delivery.
func hammer(t *testing.T, write func() error) {
	t.Helper()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = write()
			}
		}()
	}
	wg.Wait()
}

func TestGELFWriterReconnectRace(t *testing.T) {
	addr, stop := flakySink(t, 3)
	defer stop()
	host, port := splitHostPort(t, addr)

	w, err := NewGELFWriter(GELFConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	hammer(t, func() error { return w.WriteEvent(context.Background(), raceEvent()) })
}

func TestSyslogWriterReconnectRace(t *testing.T) {
	addr, stop := flakySink(t, 3)
	defer stop()
	host, port := splitHostPort(t, addr)

	w, err := NewSyslogWriter(SyslogConfig{Host: host, Port: port, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	hammer(t, func() error { return w.WriteEvent(context.Background(), raceEvent()) })
}

func TestBeatsWriterReconnectRace(t *testing.T) {
	// The Beats client speaks Lumberjack and blocks in AwaitACK for the
	// writer's 30s Timeout on every Send that isn't ACKed. A sink that
	// merely discards bytes and never closes never triggers that read
	// deadline early, so every hammer() call would stall for up to 30s (60s
	// once sendWithRetry's single retry is counted) serialized behind the
	// mutex — turning this test into a multi-hour run. Reusing flakySink
	// instead closes each connection quickly, which unblocks the pending
	// AwaitACK read immediately with a connection error instead of waiting
	// out the deadline, and drives the same connect()/send() reconnect race
	// the GELF and syslog tests exercise.
	addr, stop := flakySink(t, 1)
	defer stop()
	host, port := splitHostPort(t, addr)

	w, err := NewBeatsWriter(BeatsConfig{Host: host, Port: port})
	if err != nil {
		t.Skipf("beats writer could not dial the stub sink: %v", err)
	}
	defer func() { _ = w.Close() }()

	hammer(t, func() error { return w.WriteEvent(context.Background(), raceEvent()) })
}
