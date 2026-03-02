//go:build !windows

package evtx

// NewNativeEvtxWriter returns the BinaryEvtxWriter stub on non-Windows platforms.
// The evtxPath parameter specifies the output directory for .evtx files.
func NewNativeEvtxWriter(evtxPath string) (Writer, error) {
	return NewBinaryEvtxWriter(evtxPath)
}
