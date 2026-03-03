//go:build !windows

package main

import "context"

// runWithServiceManager runs the program directly on non-Windows platforms.
// On Windows, this function is replaced by service_windows.go,
// which wraps run() with the Windows Service Control Manager.
func runWithServiceManager(runFn func(ctx context.Context)) {
	runFn(context.Background())
}
