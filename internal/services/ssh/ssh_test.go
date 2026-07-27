package ssh

import (
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{name: "https same host", host: "panel.example.com", origin: "https://panel.example.com", want: true},
		{name: "same host and port", host: "panel.example.com:8443", origin: "https://panel.example.com:8443", want: true},
		{name: "different host", host: "panel.example.com", origin: "https://evil.example.com", want: false},
		{name: "different port", host: "panel.example.com:8443", origin: "https://panel.example.com", want: false},
		{name: "missing origin", host: "panel.example.com", origin: "", want: false},
		{name: "invalid scheme", host: "panel.example.com", origin: "file://panel.example.com", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://"+test.host+"/v1/ssh/open", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if got := sameOrigin(request); got != test.want {
				t.Fatalf("sameOrigin() = %v, want %v", got, test.want)
			}
		})
	}
}
