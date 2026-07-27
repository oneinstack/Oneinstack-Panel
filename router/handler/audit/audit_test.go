package audit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseFilterRejectsInvalidRanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(
		http.MethodGet,
		"/v1/audit/events?startAt=2026-07-26T12:00:00Z&endAt=2026-07-25T12:00:00Z",
		nil,
	)
	if _, err := parseFilter(context); err == nil {
		t.Fatal("inverted audit date range was accepted")
	}
}

func TestCSVSafePreventsSpreadsheetFormulaInjection(t *testing.T) {
	for _, value := range []string{"=cmd()", "+SUM(1,1)", "-2+3", "@IMPORTDATA(test)"} {
		if protected := csvSafe(value); !strings.HasPrefix(protected, "'") {
			t.Fatalf("unsafe CSV value %q was not protected: %q", value, protected)
		}
	}
	if value := csvSafe("normal text"); value != "normal text" {
		t.Fatalf("normal CSV value changed: %q", value)
	}
}
