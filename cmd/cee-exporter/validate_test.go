package main

import (
	"strings"
	"testing"
)

func TestValidateOutputConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     OutputConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid evtx config",
			cfg:  OutputConfig{Type: "evtx", EVTXPath: "/tmp/audit.evtx", FlushIntervalSec: 15},
		},
		{
			name:    "evtx with flush_interval_s=0",
			cfg:     OutputConfig{Type: "evtx", EVTXPath: "/tmp/audit.evtx", FlushIntervalSec: 0},
			wantErr: true,
			errMsg:  "flush_interval_s",
		},
		{
			name:    "evtx with flush_interval_s negative",
			cfg:     OutputConfig{Type: "evtx", EVTXPath: "/tmp/audit.evtx", FlushIntervalSec: -1},
			wantErr: true,
			errMsg:  "flush_interval_s",
		},
		{
			name:    "evtx with max_file_size_mb negative",
			cfg:     OutputConfig{Type: "evtx", EVTXPath: "/tmp/audit.evtx", FlushIntervalSec: 15, MaxFileSizeMB: -1},
			wantErr: true,
			errMsg:  "max_file_size_mb",
		},
		{
			name:    "evtx with max_file_count negative",
			cfg:     OutputConfig{Type: "evtx", EVTXPath: "/tmp/audit.evtx", FlushIntervalSec: 15, MaxFileCount: -1},
			wantErr: true,
			errMsg:  "max_file_count",
		},
		{
			name:    "evtx with rotation_interval_h negative",
			cfg:     OutputConfig{Type: "evtx", EVTXPath: "/tmp/audit.evtx", FlushIntervalSec: 15, RotationIntervalH: -1},
			wantErr: true,
			errMsg:  "rotation_interval_h",
		},
		{
			name:    "evtx with empty evtx_path",
			cfg:     OutputConfig{Type: "evtx", EVTXPath: "", FlushIntervalSec: 15},
			wantErr: true,
			errMsg:  "evtx_path",
		},
		{
			name: "gelf type skips evtx validation",
			cfg:  OutputConfig{Type: "gelf", FlushIntervalSec: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOutputConfig(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateOutputConfig() = nil, want error containing %q", tt.errMsg)
					return
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateOutputConfig() error = %q, want it to contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateOutputConfig() = %v, want nil", err)
				}
			}
		})
	}
}
