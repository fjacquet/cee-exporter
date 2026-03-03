//go:build windows

package main

// runWithServiceManager runs the program directly on Windows until Phase 5
// replaces this stub with a Windows Service Control Manager wrapper.
func runWithServiceManager(runFn func()) {
	runFn()
}
