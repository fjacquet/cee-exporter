//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/kardianos/service"
)

const (
	svcName        = "cee-exporter"
	svcDisplayName = "CEE Exporter"
	svcDescription = "Dell PowerStore CEPA audit event bridge to GELF / Windows Event Log"
)

// svcProgram implements service.Interface for kardianos/service.
type svcProgram struct {
	cancel context.CancelFunc        // set in Start(), called by Stop()
	runFn  func(ctx context.Context) // run() from main.go
}

func (p *svcProgram) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.runFn(ctx) // must not block — Start() must return immediately
	return nil
}

func (p *svcProgram) Stop(s service.Service) error {
	// Bridge SCM stop control into run()'s context cancellation.
	// Windows SCM does NOT send POSIX signals — this is the only shutdown path.
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

// svcConfig builds service.Config with correct Windows options.
func svcConfig(cfgPath string) *service.Config {
	return &service.Config{
		Name:        svcName,
		DisplayName: svcDisplayName,
		Description: svcDescription,
		// Store -config path so SCM replays it on boot.
		// DO NOT include "install"/"uninstall" subcommand here.
		Arguments: []string{"-config", cfgPath},
		Option: service.KeyValue{
			// Delayed Start: 120s after automatic services — avoids SCM 30s timeout
			// during boot when Go runtime init is slow (golang/go#23479).
			"StartType":        "automatic",
			"DelayedAutoStart": true,
			// Recovery: restart 5s after any failure; reset count after 24h.
			// kardianos/service also calls SetRecoveryActionsOnNonCrashFailures(true)
			// so non-zero os.Exit() also triggers restart, not just crashes.
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "5s",
			"OnFailureResetPeriod":   86400,
		},
	}
}

// runWithServiceManager is the entry point called by main().
// On Windows, it wraps run() with the Windows Service Control Manager.
func runWithServiceManager(runFn func(ctx context.Context)) {
	// Extract -config path BEFORE subcommand dispatch, BEFORE flag.Parse() in run().
	// Pitfall: passing os.Args[1:] directly would include "install" as a service arg,
	// causing SCM to re-run install on every boot (infinite install loop).
	cfgPath := parseCfgPath(os.Args[1:])

	prg := &svcProgram{runFn: runFn}

	s, err := service.New(prg, svcConfig(cfgPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "service.New: %v\n", err)
		os.Exit(1)
	}

	// Subcommand dispatch: check first arg before flag.Parse() in run().
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			if err := s.Install(); err != nil {
				fmt.Fprintf(os.Stderr, "install failed: %v\n", err)
				fmt.Fprintln(os.Stderr, "Note: install requires Administrator privileges.")
				os.Exit(1)
			}
			fmt.Printf("Service %q installed (Automatic Delayed Start). Start with: sc start %s\n", svcName, svcName)
			return
		case "uninstall":
			if err := s.Uninstall(); err != nil {
				fmt.Fprintf(os.Stderr, "uninstall failed: %v\n", err)
				fmt.Fprintln(os.Stderr, "Note: uninstall requires Administrator privileges.")
				os.Exit(1)
			}
			fmt.Printf("Service %q uninstalled.\n", svcName)
			return
		}
	}

	// Not install/uninstall — run as Windows service (when started by SCM)
	// or interactively from a console window.
	if err := s.Run(); err != nil {
		slog.Error("service_run_error", "error", err)
		os.Exit(1)
	}
}
