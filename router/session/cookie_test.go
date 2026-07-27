package session

import (
	"net/http/httptest"
	"oneinstack/app"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWriteIgnoresHTTPSHeaderFromUntrustedPeer(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "http://panel.example.com/v1/login", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request

	Write(context, "secret")

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("cookie flags = %#v", cookies)
	}
}

func TestWriteUsesSecureCookieBehindTrustedHTTPSProxy(t *testing.T) {
	original := append([]string(nil), app.ONE_CONFIG.System.TrustedProxies...)
	app.ONE_CONFIG.System.TrustedProxies = []string{"192.0.2.10"}
	t.Cleanup(func() {
		app.ONE_CONFIG.System.TrustedProxies = original
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "http://panel.example.com/v1/login", nil)
	request.RemoteAddr = "192.0.2.10:443"
	request.Header.Set("X-Forwarded-Proto", "https")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request

	Write(context, "secret")

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("cookie flags = %#v", cookies)
	}
}
