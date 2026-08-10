// emit_test_events_test.go — the -emit-test-events path's exit-code contract.
//
// White-box: package main. stdlib only.
package main

import (
	"errors"
	"testing"
)

// TestEmitExitCode covers all four combinations, because the one that was
// wrong is the one nobody thinks about: emit succeeded, Close failed. Close
// finalises the .evtx chunk, so that combination can leave an unfinalised
// file — and the evtx-oracle CI job trusts this exit code before it parses
// the artifact. It used to return 0.
func TestEmitExitCode(t *testing.T) {
	emitFail := errors.New("emit failed")
	closeFail := errors.New("close failed")

	tests := []struct {
		name     string
		emitErr  error
		closeErr error
		want     int
	}{
		{"both succeeded", nil, nil, 0},
		{"emit failed", emitFail, nil, 1},
		{"close failed", nil, closeFail, 1},
		{"both failed", emitFail, closeFail, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emitExitCode(tt.emitErr, tt.closeErr); got != tt.want {
				t.Errorf("emitExitCode(%v, %v) = %d, want %d", tt.emitErr, tt.closeErr, got, tt.want)
			}
		})
	}
}
