package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestLive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	Live(context)

	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected liveness response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

func TestCheckReadiness(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:health-tests?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}

	ready, checks := checkReadiness(context.Background(), db, func(name string) ([]byte, error) {
		if name != "index.html" {
			t.Fatalf("read file %q, want index.html", name)
		}
		return []byte("<!doctype html>"), nil
	})

	if !ready || checks["database"] != "ok" || checks["webui"] != "ok" {
		t.Fatalf("healthy dependencies reported unavailable: ready=%v checks=%v", ready, checks)
	}
}

func TestCheckReadinessReportsUnavailableWithoutLeakingErrors(t *testing.T) {
	ready, checks := checkReadiness(context.Background(), nil, func(string) ([]byte, error) {
		return nil, errors.New("sensitive filesystem detail")
	})

	if ready {
		t.Fatal("unavailable dependencies reported ready")
	}
	if checks["database"] != "unavailable" || checks["webui"] != "unavailable" {
		t.Fatalf("unexpected checks: %v", checks)
	}
}
