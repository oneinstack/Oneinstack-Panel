package cluster

import (
	"testing"
)

func TestNormalizeControllerURL(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantURL     string
		wantStrip   bool
	}{
		{
			name:        "basic URL without v1",
			input:       "http://controller.example.com:8089",
			wantURL:     "http://controller.example.com:8089",
			wantStrip:   false,
		},
		{
			name:        "URL with trailing /v1",
			input:       "http://controller.example.com:8089/v1",
			wantURL:     "http://controller.example.com:8089",
			wantStrip:   true,
		},
		{
			name:        "URL with trailing /v1/",
			input:       "http://controller.example.com:8089/v1/",
			wantURL:     "http://controller.example.com:8089",
			wantStrip:   true,
		},
		{
			name:        "HTTPS URL with trailing /v1",
			input:       "https://controller.example.com/v1",
			wantURL:     "https://controller.example.com",
			wantStrip:   true,
		},
		{
			name:        "URL with trailing slash but no v1",
			input:       "http://controller.example.com:8089/",
			wantURL:     "http://controller.example.com:8089",
			wantStrip:   false,
		},
		{
			name:        "URL with whitespace",
			input:       "  http://controller.example.com:8089  ",
			wantURL:     "http://controller.example.com:8089",
			wantStrip:   false,
		},
		{
			name:        "URL with whitespace and /v1",
			input:       "  http://controller.example.com:8089/v1  ",
			wantURL:     "http://controller.example.com:8089",
			wantStrip:   true,
		},
		{
			name:        "URL with path that ends with v1 but is not /v1",
			input:       "http://controller.example.com:8089/api/v1",
			wantURL:     "http://controller.example.com:8089/api/v1",
			wantStrip:   false,
		},
		{
			name:        "empty string",
			input:       "",
			wantURL:     "",
			wantStrip:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotStrip := NormalizeControllerURL(tt.input)
			if gotURL != tt.wantURL {
				t.Errorf("NormalizeControllerURL() URL = %q, want %q", gotURL, tt.wantURL)
			}
			if gotStrip != tt.wantStrip {
				t.Errorf("NormalizeControllerURL() stripped = %v, want %v", gotStrip, tt.wantStrip)
			}
		})
	}
}
