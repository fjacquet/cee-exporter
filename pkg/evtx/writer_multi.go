// Multi-target writer — all platforms.
//
// MultiWriter fans an event out to a list of Writers in order.  All targets
// receive the event; the first error is returned (remaining targets still
// receive the event).
package evtx

import (
	"context"
	"errors"
)

// MultiWriter fans events out to multiple backends.
type MultiWriter struct {
	writers []Writer
}

// NewMultiWriter wraps zero or more writers.
func NewMultiWriter(writers ...Writer) *MultiWriter {
	return &MultiWriter{writers: writers}
}

// WriteEvent sends the event to every backend.  All targets are called even
// if an earlier one errors.  All errors are joined.
func (m *MultiWriter) WriteEvent(ctx context.Context, e WindowsEvent) error {
	var errs []error
	for _, w := range m.writers {
		if err := w.WriteEvent(ctx, e); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close closes all backends and returns the joined errors.
func (m *MultiWriter) Close() error {
	var errs []error
	for _, w := range m.writers {
		if err := w.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
