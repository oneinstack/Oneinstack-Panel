package cluster

import (
	"net"
	"testing"

	"oneinstack/internal/models"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		private bool
	}{
		// Private IPv4 ranges
		{"10.0.0.1", "10.0.0.1", true},
		{"10.255.255.255", "10.255.255.255", true},
		{"172.16.0.1", "172.16.0.1", true},
		{"172.31.255.255", "172.31.255.255", true},
		{"172.15.0.1 (not private)", "172.15.0.1", false},
		{"172.32.0.1 (not private)", "172.32.0.1", false},
		{"192.168.0.1", "192.168.0.1", true},
		{"192.168.255.255", "192.168.255.255", true},
		{"100.64.0.1 (CGNAT)", "100.64.0.1", true},
		{"100.127.255.255 (CGNAT)", "100.127.255.255", true},
		{"100.63.255.255 (not CGNAT)", "100.63.255.255", false},
		{"100.128.0.1 (not CGNAT)", "100.128.0.1", false},

		// Public IPv4
		{"8.8.8.8", "8.8.8.8", false},
		{"35.192.0.1", "35.192.0.1", false},
		{"203.0.113.1", "203.0.113.1", false},

		// IPv6 unique local
		{"fc00::1", "fc00::1", true},
		{"fd12:3456:789a::1", "fd12:3456:789a::1", true},
		{"2001:db8::1 (not private)", "2001:db8::1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP: %s", tt.ip)
			}
			got := isPrivateIP(ip)
			if got != tt.private {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.private)
			}
		})
	}
}

func TestAnalyzeEndpointAddress(t *testing.T) {
	tests := []struct {
		name          string
		endpoint      string
		ipAddress     string
		status        string
		wantMismatch  bool
		wantNoteEmpty bool
	}{
		{
			name:          "hostname endpoint - no mismatch",
			endpoint:      "http://controller.example.com:8089",
			ipAddress:     "192.168.1.10",
			status:        models.ClusterNodeStatusOnline,
			wantMismatch:  false,
			wantNoteEmpty: true,
		},
		{
			name:          "matching IPs - no mismatch",
			endpoint:      "http://192.168.1.10:8089",
			ipAddress:     "192.168.1.10",
			status:        models.ClusterNodeStatusOnline,
			wantMismatch:  false,
			wantNoteEmpty: true,
		},
		{
			name:          "public endpoint, private reported, online - NAT OK",
			endpoint:      "http://35.192.0.1:8089",
			ipAddress:     "192.168.1.10",
			status:        models.ClusterNodeStatusOnline,
			wantMismatch:  false,
			wantNoteEmpty: false,
		},
		{
			name:          "public endpoint, private reported, offline - problem",
			endpoint:      "http://35.192.0.1:8089",
			ipAddress:     "192.168.1.10",
			status:        models.ClusterNodeStatusOffline,
			wantMismatch:  true,
			wantNoteEmpty: false,
		},
		{
			name:          "private endpoint, public reported, online",
			endpoint:      "http://192.168.1.10:8089",
			ipAddress:     "35.192.0.1",
			status:        models.ClusterNodeStatusOnline,
			wantMismatch:  false,
			wantNoteEmpty: false,
		},
		{
			name:          "different private IPs, online",
			endpoint:      "http://192.168.1.10:8089",
			ipAddress:     "10.0.0.5",
			status:        models.ClusterNodeStatusOnline,
			wantMismatch:  false,
			wantNoteEmpty: false,
		},
		{
			name:          "different private IPs, offline",
			endpoint:      "http://192.168.1.10:8089",
			ipAddress:     "10.0.0.5",
			status:        models.ClusterNodeStatusOffline,
			wantMismatch:  true,
			wantNoteEmpty: false,
		},
		{
			name:          "no reported IP yet",
			endpoint:      "http://35.192.0.1:8089",
			ipAddress:     "",
			status:        models.ClusterNodeStatusPending,
			wantMismatch:  false,
			wantNoteEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := models.ClusterNode{
				Endpoint:  tt.endpoint,
				IPAddress: tt.ipAddress,
				Status:    tt.status,
			}
			gotMismatch, gotNote := analyzeEndpointAddress(node)
			if gotMismatch != tt.wantMismatch {
				t.Errorf("analyzeEndpointAddress() mismatch = %v, want %v", gotMismatch, tt.wantMismatch)
			}
			gotNoteEmpty := gotNote == ""
			if gotNoteEmpty != tt.wantNoteEmpty {
				t.Errorf("analyzeEndpointAddress() note empty = %v, want %v (note: %q)", gotNoteEmpty, tt.wantNoteEmpty, gotNote)
			}
		})
	}
}
