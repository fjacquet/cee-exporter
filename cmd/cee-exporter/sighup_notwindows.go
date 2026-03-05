//go:build !windows

package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/fjacquet/cee-exporter/pkg/evtx"
)

// installSIGHUP starts a goroutine that listens for SIGHUP and triggers
// an immediate .evtx file rotation on the writer if it supports it.
// Only BinaryEvtxWriter satisfies the Rotate() interface; other writers
// are silently skipped via type assertion.
func installSIGHUP(w evtx.Writer) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		for range ch {
			slog.Info("sighup_received")
			if rotator, ok := w.(interface{ Rotate() error }); ok {
				if err := rotator.Rotate(); err != nil {
					slog.Error("sighup_rotate_failed", "error", err)
				} else {
					slog.Info("sighup_rotate_complete")
				}
			}
		}
	}()
}
