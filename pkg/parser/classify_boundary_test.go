package parser

import "testing"

// TestRootIs_RequiresNameBoundary: prefix-matching the root element without
// checking what follows the name accepted <CheckFileRequestExtra> as
// <CheckFileRequest>. Both consequences are silent on this protocol — the
// handler answers with one dialect's protocol document to another dialect's
// request, and then parses the payload on the wrong path.
func TestRootIs_RequiresNameBoundary(t *testing.T) {
	// Longer names that merely START with a real one must not match.
	for _, tc := range []struct {
		body string
		name string
	}{
		{`<CheckFileRequestExtra foo="1"/>`, "CheckFileRequest"},
		{`<RegisterRequestBogus/>`, "RegisterRequest"},
		{`<CheckEventRequestX><EventList count="0"/></CheckEventRequestX>`, "CheckEventRequest"},
		{`<HeartBeatRequestly />`, "HeartBeatRequest"},
		{`<CheckFileRequest`, "CheckFileRequest"}, // truncated: name never ends
	} {
		if rootIs([]byte(tc.body), tc.name) {
			t.Errorf("rootIs(%q, %q) = true; the element name does not end there", tc.body, tc.name)
		}
	}

	// Every legal delimiter after the name must still match.
	for _, body := range []string{
		`<CheckFileRequest>`,
		`<CheckFileRequest/>`,
		`<CheckFileRequest />`,
		"<CheckFileRequest\taction=\"9\">",
		"<CheckFileRequest\n  action=\"9\">",
		"<CheckFileRequest\r\n/>",
		`<?xml version="1.0" encoding="UTF-8"?><CheckFileRequest action="9">`,
	} {
		if !rootIs([]byte(body), "CheckFileRequest") {
			t.Errorf("rootIs(%q) = false; this is a well-formed CheckFileRequest root", body)
		}
	}
}

// TestClassify_RejectsLookalikeRoots is the same guard one level up, where the
// consequence actually lives: a lookalike must reach no dialect branch.
func TestClassify_RejectsLookalikeRoots(t *testing.T) {
	for _, body := range []string{
		`<CheckFileRequestExtra foo="1"/>`,
		`<RegisterRequestBogus/>`,
		`<CheckEventRequestX/>`,
		`<HeartBeatRequestly/>`,
	} {
		d, _, err := Classify([]byte(body))
		if err != nil {
			t.Fatalf("Classify(%q) errored: %v", body, err)
		}
		if d != DialectUnknown {
			t.Errorf("Classify(%q) = dialect %d, want DialectUnknown", body, d)
		}
	}
}
