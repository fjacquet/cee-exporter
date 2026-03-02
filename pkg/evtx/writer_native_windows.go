//go:build windows

package evtx

// NewNativeEvtxWriter returns the Win32 EventLog writer on Windows.
func NewNativeEvtxWriter(_ string) (Writer, error) {
	return NewWin32EventLogWriter()
}
