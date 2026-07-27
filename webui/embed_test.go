package webui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedFrontendContainsProductionIndex(t *testing.T) {
	content, err := ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !bytes.Contains(bytes.ToLower(content), []byte("<!doctype html>")) {
		t.Fatal("embedded index is not an HTML document")
	}

	references := regexp.MustCompile(`(?:src|href)="(/[^"]+)"`).FindAllSubmatch(content, -1)
	if len(references) == 0 {
		t.Fatal("embedded index does not reference any production assets")
	}
	for _, reference := range references {
		asset := strings.TrimPrefix(string(reference[1]), "/")
		if _, err := ReadFile(asset); err != nil {
			t.Fatalf("index references missing asset %q: %v", asset, err)
		}
	}
}

func TestReadFileRejectsTraversal(t *testing.T) {
	if _, err := ReadFile("../config.yaml"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}
