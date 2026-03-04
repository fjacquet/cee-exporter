//go:build !windows

package evtx

import goevtx "github.com/fjacquet/go-evtx"

// NewNativeEvtxWriter returns the BinaryEvtxWriter on non-Windows platforms.
//
// cfg controls the periodic checkpoint-write goroutine inside go-evtx.
// Pass goevtx.RotationConfig{} to disable the background goroutine.
func NewNativeEvtxWriter(evtxPath string, cfg goevtx.RotationConfig) (Writer, error) {
	return NewBinaryEvtxWriter(evtxPath, cfg)
}
