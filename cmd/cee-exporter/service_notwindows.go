//go:build !windows

package main

// runWithServiceManager runs the program directly on non-Windows platforms.
// On Windows, this function is replaced by service_windows.go (Phase 5),
// which wraps run() with the Windows Service Control Manager.
func runWithServiceManager(runFn func()) {
	runFn()
}
