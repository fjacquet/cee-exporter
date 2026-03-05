//go:build windows

package main

import "github.com/fjacquet/cee-exporter/pkg/evtx"

// installSIGHUP is a no-op on Windows (SIGHUP is not a Windows signal).
func installSIGHUP(_ evtx.Writer) {}
