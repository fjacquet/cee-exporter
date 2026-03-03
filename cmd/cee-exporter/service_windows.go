//go:build windows

package main

import "context"

// runWithServiceManager runs the program directly on Windows until Phase 5 Plan 03
// replaces this stub with a Windows Service Control Manager wrapper.
func runWithServiceManager(runFn func(ctx context.Context)) {
	runFn(context.Background())
}
