package vm

import (
	"strings"
	"testing"
)

func TestParseHostKeys(t *testing.T) {
	const ip = "192.168.122.42"
	const ed = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID root@host"
	const ecdsa = "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTI root@host"
	const rsa = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQAB root@host"
	const dsa = "ssh-dss AAAAB3NzaC1kc3MAAACB root@host"

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "all three key types, comment stripped",
			input: strings.Join([]string{ed, ecdsa, rsa}, "\n"),
			want: strings.Join([]string{
				ip + " ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID",
				ip + " ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTI",
				ip + " ssh-rsa AAAAB3NzaC1yc2EAAAADAQAB",
			}, "\n") + "\n",
		},
		{
			name:  "single key without comment",
			input: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID",
			want:  ip + " ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID\n",
		},
		{
			name:  "deprecated ssh-dss is filtered out",
			input: strings.Join([]string{dsa, ed}, "\n"),
			want:  ip + " ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID\n",
		},
		{
			name:    "only ssh-dss yields no-keys error",
			input:   dsa,
			wantErr: true,
		},
		{
			name:  "blank and malformed lines skipped",
			input: "\n  \nbogus\n" + ed + "\n",
			want:  ip + " ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID\n",
		},
		{
			name:    "empty input yields no-keys error",
			input:   "",
			wantErr: true,
		},
		{
			name:    "only malformed input yields no-keys error",
			input:   "bogus\nalsobogus",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHostKeys(tt.input, ip)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (out=%q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}
