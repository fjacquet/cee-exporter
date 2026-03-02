package parser

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	receiveTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		input    string
		wantLen  int
		wantErr  bool
	}{
		{
			name:    "single_event",
			input:   `<CEEEvent><EventType>CEPP_FILE_WRITE</EventType><FilePath>/share/test.txt</FilePath><Username>alice</Username><Domain>CORP</Domain><ClientAddress>10.0.0.1</ClientAddress></CEEEvent>`,
			wantLen: 1,
			wantErr: false,
		},
		{
			name:    "vcaps_batch_two_events",
			input:   `<EventBatch><CEEEvent><EventType>CEPP_CREATE_FILE</EventType><FilePath>/a</FilePath></CEEEvent><CEEEvent><EventType>CEPP_DELETE_FILE</EventType><FilePath>/b</FilePath></CEEEvent></EventBatch>`,
			wantLen: 2,
			wantErr: false,
		},
		{
			name:    "malformed_xml",
			input:   `not xml at all`,
			wantLen: 0,
			wantErr: true,
		},
		{
			name:    "empty_payload",
			input:   ``,
			wantLen: 0,
			wantErr: true,
		},
		{
			name:    "wrong_root_element",
			input:   `<UnknownRoot><SomeField>x</SomeField></UnknownRoot>`,
			wantLen: 0,
			wantErr: true,
		},
		{
			name:    "single_event_with_timestamp",
			input:   `<CEEEvent><EventType>CEPP_FILE_READ</EventType><Timestamp>2024-01-15T10:30:00Z</Timestamp></CEEEvent>`,
			wantLen: 1,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := Parse([]byte(tt.input), receiveTime)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Parse(%q) expected error, got nil", tt.name)
				}
				if len(events) != tt.wantLen {
					t.Errorf("Parse(%q) events length = %d, want %d", tt.name, len(events), tt.wantLen)
				}
				return
			}

			if err != nil {
				t.Errorf("Parse(%q) unexpected error: %v", tt.name, err)
				return
			}

			if len(events) != tt.wantLen {
				t.Errorf("Parse(%q) events length = %d, want %d", tt.name, len(events), tt.wantLen)
				return
			}

			// Per-case field assertions
			switch tt.name {
			case "single_event":
				if events[0].EventType != "CEPP_FILE_WRITE" {
					t.Errorf("single_event EventType = %q, want %q", events[0].EventType, "CEPP_FILE_WRITE")
				}
				if events[0].FilePath != "/share/test.txt" {
					t.Errorf("single_event FilePath = %q, want %q", events[0].FilePath, "/share/test.txt")
				}

			case "single_event_with_timestamp":
				if events[0].Timestamp.IsZero() {
					t.Errorf("single_event_with_timestamp Timestamp is zero, expected parsed time")
				}
			}
		})
	}
}

func TestIsRegisterRequest(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "register_request_tag",
			input: `<RegisterRequest />`,
			want:  true,
		},
		{
			name:  "register_request_with_whitespace",
			input: `  <RegisterRequest/>  `,
			want:  true,
		},
		{
			name:  "event_payload",
			input: `<CEEEvent><EventType>CEPP_FILE_WRITE</EventType></CEEEvent>`,
			want:  false,
		},
		{
			name:  "empty",
			input: ``,
			want:  false,
		},
		{
			name:  "unrelated_xml",
			input: `<SomeOtherElement/>`,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRegisterRequest([]byte(tt.input))
			if got != tt.want {
				t.Errorf("IsRegisterRequest(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
