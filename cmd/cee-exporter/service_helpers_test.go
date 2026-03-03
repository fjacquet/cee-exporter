package main

import "testing"

func TestParseCfgPath(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no args returns default",
			args: []string{},
			want: "config.toml",
		},
		{
			name: "short flag -config",
			args: []string{"-config", "myapp.toml"},
			want: "myapp.toml",
		},
		{
			name: "long flag --config with absolute path",
			args: []string{"--config", "/etc/app.toml"},
			want: "/etc/app.toml",
		},
		{
			name: "install subcommand ignored",
			args: []string{"install", "-config", "prod.toml"},
			want: "prod.toml",
		},
		{
			name: "dangling flag returns default",
			args: []string{"-config"},
			want: "config.toml",
		},
		{
			name: "other flags ignored",
			args: []string{"--verbose", "-config", "x.toml"},
			want: "x.toml",
		},
		{
			name: "uninstall subcommand returns default",
			args: []string{"uninstall"},
			want: "config.toml",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCfgPath(tc.args)
			if got != tc.want {
				t.Errorf("parseCfgPath(%v) = %q; want %q", tc.args, got, tc.want)
			}
		})
	}
}
