//go:build windows

package evtx

import goevtx "github.com/fjacquet/go-evtx"

// NewNativeEvtxWriter returns the Win32 EventLog writer on Windows.
// The cfg parameter is accepted for API compatibility with the non-Windows
// implementation but has no effect — Win32 EventLog is synchronous and
// does not require periodic checkpoint writes.
func NewNativeEvtxWriter(_ string, _ goevtx.RotationConfig) (Writer, error) {
	return NewWin32EventLogWriter()
}
