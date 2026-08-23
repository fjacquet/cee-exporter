package parser

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// benchBatch builds a <CheckEventRequest> of n events from the attribute list
// pinned in checkevent_test.go, which was recovered from
// CCheckEventRequest::GetXmlRequest() in the vendored CEE 9.2.0.0 rpm. Paths
// vary per event so the parser is not measured against one cached string.
func benchBatch(n int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<CheckEventRequest><EventList count="%d">`, n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<Event event="0x00000008" path="\\NAS01\fs01\dir%d\file%d.txt" flag="0x0" `+
			`server="NAS01" share="fs01" clientIP="10.26.1.222" serverIP="10.26.1.224" `+
			`timeStamp="1786735002" userSid="S-1-5-21-1-2-3-1001" ownerSid="S-1-5-21-1-2-3-513" `+
			`fileSize="0x400" newName="" desiredAccess="0x100106" createDispo="0x3" `+
			`ntStatus="0x0" relativePath="\dir%d\file%d.txt"/>`, i%50, i, i%50, i)
	}
	b.WriteString(`</EventList></CheckEventRequest>`)
	return []byte(b.String())
}

// Divide ns/op by the event count for the per-event figure. Measured on an
// M1 Pro before this work: 8.6 µs/event UTF-8 and 10.9 µs/event UTF-16LE at
// 1000 events, 73 allocs/event in both.
func BenchmarkParseCheckEventRequest(b *testing.B) {
	for _, n := range []int{1, 100, 1000} {
		body := benchBatch(n)
		b.Run(fmt.Sprintf("utf8/events=%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				_, decoded, err := Classify(body)
				if err != nil {
					b.Fatal(err)
				}
				ev, err := ParseCheckEventRequestDecoded(decoded, time.Now())
				if err != nil {
					b.Fatal(err)
				}
				if len(ev) != n {
					b.Fatalf("parsed %d events, want %d", len(ev), n)
				}
			}
		})
	}
}

// The UTF-16LE path allocates a second whole copy of the body during
// transcode — 14.6 KB/event against 4.0 measured — and CEE sends UTF-16LE to
// an unregistered partner, so it is not a rare path.
func BenchmarkParseCheckEventRequestUTF16(b *testing.B) {
	const n = 1000
	body := EncodeUTF16LE(benchBatch(n))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		_, decoded, err := Classify(body)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := ParseCheckEventRequestDecoded(decoded, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
