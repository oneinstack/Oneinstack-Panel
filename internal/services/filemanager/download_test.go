package filemanager

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestValidateRemoteURL(t *testing.T) {
	accepted := []string{
		"https://example.com/archive.tar.gz",
		"http://8.8.8.8/file",
	}
	for _, rawURL := range accepted {
		remoteURL, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateRemoteURL(remoteURL); err != nil {
			t.Errorf("ValidateRemoteURL(%q) error = %v", rawURL, err)
		}
	}

	rejected := []string{
		"file:///etc/passwd",
		"ftp://example.com/file",
		"http://user:password@example.com/file",
		"http://localhost/file",
		"http://api.localhost/file",
		"http://127.0.0.1/file",
		"http://10.0.0.1/file",
		"http://169.254.169.254/latest/meta-data",
		"http://100.64.0.1/file",
		"http://[::1]/file",
		"http://[fc00::1]/file",
	}
	for _, rawURL := range rejected {
		remoteURL, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateRemoteURL(remoteURL); err == nil {
			t.Errorf("ValidateRemoteURL(%q) should reject the URL", rawURL)
		}
	}
}

func TestPublicAddressClassification(t *testing.T) {
	if !isPublicAddress(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("8.8.8.8 should be a public address")
	}
	for _, rawAddress := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "100.64.0.1", "::1", "fc00::1"} {
		if isPublicAddress(netip.MustParseAddr(rawAddress)) {
			t.Errorf("%s should not be a public address", rawAddress)
		}
	}
}

func TestDownloaderWritesFileWithinRoot(t *testing.T) {
	manager, rootPath := newTestManager(t)
	if err := manager.MkdirAll("/downloads", 0755); err != nil {
		t.Fatal(err)
	}
	downloader := testDownloader(http.StatusOK, "hello", 32)

	if err := downloader.Download(context.Background(), manager, "https://example.com/file", "/downloads", "file.txt"); err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	content, err := os.ReadFile(rootPath + "/downloads/file.txt")
	if err != nil || string(content) != "hello" {
		t.Fatalf("content=%q error=%v", content, err)
	}
}

func TestDownloaderRejectsOversizedAndBadResponses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		limit      int64
	}{
		{name: "oversized", statusCode: http.StatusOK, body: "too large", limit: 3},
		{name: "bad status", statusCode: http.StatusNotFound, body: "missing", limit: 32},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, rootPath := newTestManager(t)
			downloader := testDownloader(test.statusCode, test.body, test.limit)

			err := downloader.Download(context.Background(), manager, "https://example.com/file", "/", "file.txt")
			if err == nil {
				t.Fatal("Download() should fail")
			}
			if _, statErr := os.Stat(rootPath + "/file.txt"); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial file was not cleaned up: %v", statErr)
			}
		})
	}
}

func TestDownloaderHonorsRequestTimeout(t *testing.T) {
	manager, rootPath := newTestManager(t)
	client := &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}),
		Timeout: 20 * time.Millisecond,
	}
	downloader := &Downloader{client: client, maxBytes: 32}

	startedAt := time.Now()
	err := downloader.Download(context.Background(), manager, "https://example.com/file", "/", "file.txt")
	if err == nil {
		t.Fatal("Download() should time out")
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("Download() ignored the HTTP timeout: %s", elapsed)
	}
	if _, statErr := os.Stat(rootPath + "/file.txt"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("timed out download created a file: %v", statErr)
	}
}

func testDownloader(statusCode int, body string, limit int64) *Downloader {
	return &Downloader{
		client: &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    statusCode,
					Status:        http.StatusText(statusCode),
					Header:        make(http.Header),
					Body:          io.NopCloser(strings.NewReader(body)),
					ContentLength: -1,
				}, nil
			}),
		},
		maxBytes: limit,
	}
}
