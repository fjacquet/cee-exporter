package evtx

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeWriter records calls for assertions and can be configured to fail on
// WriteEvent, Close, or Rotate.
type fakeWriter struct {
	writeErr  error
	closeErr  error
	rotateErr error
	written   int
	closed    int
	rotated   int
}

func (f *fakeWriter) WriteEvent(_ context.Context, _ WindowsEvent) error {
	f.written++
	return f.writeErr
}

func (f *fakeWriter) Close() error {
	f.closed++
	return f.closeErr
}

// nonRotator embeds *fakeWriter but deliberately does not expose Rotate.
// It is used to verify MultiWriter.Rotate skips writers without rotation
// support instead of erroring.
type nonRotator struct {
	*fakeWriter
}

// rotator embeds *fakeWriter and exposes Rotate.
type rotator struct {
	*fakeWriter
}

func (r *rotator) Rotate() error {
	r.rotated++
	return r.rotateErr
}

func TestMultiWriter_WriteEvent_AllCalledJoinedErrors(t *testing.T) {
	a := &fakeWriter{}
	errB := errors.New("boom-b")
	b := &fakeWriter{writeErr: errB}
	errC := errors.New("boom-c")
	c := &fakeWriter{writeErr: errC}

	mw := NewMultiWriter(a, b, c)
	err := mw.WriteEvent(context.Background(), WindowsEvent{EventID: 1})

	if a.written != 1 || b.written != 1 || c.written != 1 {
		t.Fatalf("expected every writer called once; got a=%d b=%d c=%d",
			a.written, b.written, c.written)
	}
	if err == nil {
		t.Fatal("expected joined error, got nil")
	}
	if !errors.Is(err, errB) || !errors.Is(err, errC) {
		t.Fatalf("expected joined err to wrap both; got %v", err)
	}
}

func TestMultiWriter_WriteEvent_NoErrors(t *testing.T) {
	a := &fakeWriter{}
	b := &fakeWriter{}
	if err := NewMultiWriter(a, b).WriteEvent(context.Background(), WindowsEvent{}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestMultiWriter_Close_JoinsErrors(t *testing.T) {
	errX := errors.New("close-x")
	a := &fakeWriter{closeErr: errX}
	b := &fakeWriter{}
	err := NewMultiWriter(a, b).Close()
	if a.closed != 1 || b.closed != 1 {
		t.Fatalf("expected both Close calls; got a=%d b=%d", a.closed, b.closed)
	}
	if !errors.Is(err, errX) {
		t.Fatalf("expected joined err to wrap close-x; got %v", err)
	}
}

func TestMultiWriter_Rotate_ForwardsAndSkips(t *testing.T) {
	r1 := &rotator{fakeWriter: &fakeWriter{}}
	r2 := &rotator{fakeWriter: &fakeWriter{rotateErr: errors.New("rotate-fail")}}
	skip := &nonRotator{fakeWriter: &fakeWriter{}}

	mw := NewMultiWriter(r1, r2, skip)
	err := mw.Rotate()

	if r1.rotated != 1 || r2.rotated != 1 {
		t.Fatalf("rotators must be called; r1=%d r2=%d", r1.rotated, r2.rotated)
	}
	if err == nil || !strings.Contains(err.Error(), "rotate-fail") {
		t.Fatalf("expected rotate-fail joined; got %v", err)
	}
}
