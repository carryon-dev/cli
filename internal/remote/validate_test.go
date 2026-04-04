package remote

import (
	"testing"
)

// TestValidateCandidate verifies that validateCandidate rejects invalid
// addresses and accepts legitimate routable ones.
func TestValidateCandidate(t *testing.T) {
	tests := []struct {
		name      string
		candidate Candidate
		wantErr   bool
	}{
		// Rejected: loopback
		{
			name:      "loopback IPv4",
			candidate: Candidate{Addr: "127.0.0.1", Port: 8080},
			wantErr:   true,
		},
		{
			name:      "loopback range 127.0.0.2",
			candidate: Candidate{Addr: "127.0.0.2", Port: 8080},
			wantErr:   true,
		},
		// Rejected: unspecified
		{
			name:      "unspecified 0.0.0.0",
			candidate: Candidate{Addr: "0.0.0.0", Port: 8080},
			wantErr:   true,
		},
		// Rejected: multicast
		{
			name:      "multicast 224.0.0.1",
			candidate: Candidate{Addr: "224.0.0.1", Port: 8080},
			wantErr:   true,
		},
		// Rejected: invalid IP string
		{
			name:      "invalid IP",
			candidate: Candidate{Addr: "not-an-ip", Port: 8080},
			wantErr:   true,
		},
		// Rejected: invalid port (zero)
		{
			name:      "zero port",
			candidate: Candidate{Addr: "192.168.1.10", Port: 0},
			wantErr:   true,
		},
		// Rejected: port out of range
		{
			name:      "port too high",
			candidate: Candidate{Addr: "192.168.1.10", Port: 65536},
			wantErr:   true,
		},
		// Accepted: private RFC 1918 (LAN P2P connections use these)
		{
			name:      "private 192.168.x.x",
			candidate: Candidate{Addr: "192.168.1.10", Port: 8080},
			wantErr:   false,
		},
		{
			name:      "private 10.x.x.x",
			candidate: Candidate{Addr: "10.0.0.5", Port: 9000},
			wantErr:   false,
		},
		{
			name:      "private 172.16.x.x",
			candidate: Candidate{Addr: "172.16.0.1", Port: 4433},
			wantErr:   false,
		},
		// Accepted: public routable IP
		{
			name:      "public IP",
			candidate: Candidate{Addr: "8.8.8.8", Port: 4433},
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCandidate(tt.candidate)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCandidate(%v) error = %v, wantErr %v", tt.candidate, err, tt.wantErr)
			}
		})
	}
}
