package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"oneinstack/app"
	"oneinstack/internal/crypto"
	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"
	logservice "oneinstack/internal/services/log"
	securityservice "oneinstack/internal/services/security"
	sshservice "oneinstack/internal/services/ssh"
	"oneinstack/utils"
)

var publicRoutes = map[string]struct{}{
	http.MethodPost + " /v1/login":                     {},
	http.MethodGet + " /v1/panel-entry/status":         {},
	http.MethodGet + " /v1/sys/getbaseinfo":            {},
	http.MethodGet + " /v1/public/file-share/download": {},
}

func TestMain(m *testing.M) {
	if err := app.InitDB("file:router-tests?mode=memory&cache=shared"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestManagementRoutesRequireAuthentication(t *testing.T) {
	router := SetupRouter()
	checked := 0

	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, public := publicRoutes[key]; public {
			continue
		}
		if !strings.HasPrefix(route.Path, "/v1/") {
			continue
		}

		t.Run(key, func(t *testing.T) {
			path := strings.ReplaceAll(route.Path, ":id", "1")
			req := httptest.NewRequest(route.Method, path, nil)
			// Each route is an independent authentication assertion. Use a
			// different source IP so the production API limiter does not turn a
			// large route catalog into a false 429 failure.
			req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", checked+1)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected status %d, got %d; response=%s",
					http.StatusUnauthorized, recorder.Code, recorder.Body.String())
			}

			var response struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Code != "MISSING_TOKEN" {
				t.Fatalf("expected MISSING_TOKEN, got %q", response.Code)
			}
		})
		checked++
	}

	if checked == 0 {
		t.Fatal("expected at least one protected management route")
	}
}

func TestPublicRoutesDoNotRequireAuthentication(t *testing.T) {
	router := SetupRouter()

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "login",
			method: http.MethodPost,
			path:   "/v1/login",
			body:   "{}",
		},
		{
			name:   "base info",
			method: http.MethodGet,
			path:   "/v1/sys/getbaseinfo",
		},
		{
			name:   "panel entry status",
			method: http.MethodGet,
			path:   "/v1/panel-entry/status",
		},
		{
			name:   "public file share download",
			method: http.MethodGet,
			path:   "/v1/public/file-share/download",
		},
		{
			name:   "liveness",
			method: http.MethodGet,
			path:   "/health/live",
		},
		{
			name:   "readiness",
			method: http.MethodGet,
			path:   "/health/ready",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			if recorder.Code == http.StatusUnauthorized {
				t.Fatalf("public route unexpectedly requires authentication: %s", recorder.Body.String())
			}
		})
	}
}

func TestPublicBaseInfoReturnsOnlyTitle(t *testing.T) {
	router := SetupRouter()
	if err := app.DB().Exec("DELETE FROM system").Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Create(&models.System{Title: "Secure Panel"}).Error; err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/sys/getbaseinfo", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if title, ok := response.Data["title"].(string); !ok || title != "Secure Panel" {
		t.Fatalf("title = %#v, want %q", response.Data["title"], "Secure Panel")
	}
	for _, forbidden := range []string{"id", "created_at", "updated_at"} {
		if _, exists := response.Data[forbidden]; exists {
			t.Fatalf("public base info unexpectedly includes %q: %#v", forbidden, response.Data)
		}
	}
}

func TestSSHRouteRejectsLegacyQueryToken(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "test-only-jwt-secret-at-least-32-bytes")
	token, err := utils.GenerateJWT("admin", 1)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	router := SetupRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/ssh/open?Authorization="+token, nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d; response=%s",
			http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	}
}

func TestSSHRouteAcceptsOneTimeTicketOnlyOnce(t *testing.T) {
	previous := app.ONE_CONFIG.System.TerminalEnabled
	app.ONE_CONFIG.System.TerminalEnabled = true
	t.Cleanup(func() {
		app.ONE_CONFIG.System.TerminalEnabled = previous
	})
	var account models.User
	if err := app.DB().First(&account, 1).Error; err != nil {
		account = models.User{
			ID: 1, Username: "admin", Password: "test-password-hash",
			IsAdmin: true, SecurityVersion: 1,
		}
		if err := app.DB().Create(&account).Error; err != nil {
			t.Fatal(err)
		}
	}
	sourceSession, err := securityservice.NewSessionManager(app.DB()).Create(
		securityservice.NewSession{
			UserID: 1, Username: account.Username, RemoteIP: "192.0.2.1",
			UserAgent: "router-test", SecurityVersion: account.EffectiveSecurityVersion(),
			ExpiresAt: time.Now().Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = app.DB().Unscoped().Delete(&models.UserSession{}, "id = ?", sourceSession.ID).Error
	})
	ticket, _, err := sshservice.DefaultTickets.Issue(sshservice.TicketClaims{
		UserID: 1, Username: account.Username, ClientIP: "192.0.2.1",
		UserAgent: "router-test", SourceSessionID: sourceSession.ID,
		SecurityVersion: account.EffectiveSecurityVersion(),
	})
	if err != nil {
		t.Fatal(err)
	}

	router := SetupRouter()
	first := httptest.NewRequest(http.MethodGet, "/v1/ssh/open?ticket="+ticket, nil)
	first.RemoteAddr = "192.0.2.1:1234"
	first.Header.Set("User-Agent", "router-test")
	firstResponse := httptest.NewRecorder()
	router.ServeHTTP(firstResponse, first)
	if firstResponse.Code == http.StatusUnauthorized {
		t.Fatalf("valid ticket was rejected: %s", firstResponse.Body.String())
	}

	second := httptest.NewRequest(http.MethodGet, "/v1/ssh/open?ticket="+ticket, nil)
	second.RemoteAddr = "192.0.2.1:1234"
	second.Header.Set("User-Agent", "router-test")
	secondResponse := httptest.NewRecorder()
	router.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusUnauthorized {
		t.Fatalf("reused ticket status = %d, want 401", secondResponse.Code)
	}
}

func TestProtectedRouteAcceptsBearerToken(t *testing.T) {
	token := testToken(t)

	router := SetupRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/sys/libcount", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; response=%s",
			http.StatusOK, recorder.Code, recorder.Body.String())
	}
}

func TestProtectedRouteRejectsLegacyStatelessToken(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "test-only-jwt-secret-at-least-32-bytes")
	saveTestUser(t, "legacy-admin", "existing-password-hash")
	token, err := utils.GenerateJWT("legacy-admin", 1)
	if err != nil {
		t.Fatal(err)
	}

	router := SetupRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/sys/libcount", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized ||
		!strings.Contains(recorder.Body.String(), "SESSION_REQUIRED") {
		t.Fatalf("legacy token response: status=%d body=%s",
			recorder.Code, recorder.Body.String())
	}
}

func TestStorageRoutesRequireAdministratorRole(t *testing.T) {
	const userID int64 = 902
	if err := app.DB().Save(&models.User{
		ID:       userID,
		Username: "database-viewer",
		Password: "existing-password-hash",
		IsAdmin:  false,
	}).Error; err != nil {
		t.Fatalf("save non-admin user: %v", err)
	}
	t.Cleanup(func() {
		_ = app.DB().Delete(&models.User{}, userID).Error
	})

	token := testTokenForUser(t, "database-viewer", userID)
	router := SetupRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/storage/connlist?type=mysql", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; response=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "ADMIN_REQUIRED") {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
}

func TestSoftwareServiceRoutesRequireAdministratorRole(t *testing.T) {
	const userID int64 = 912
	if err := app.DB().Save(&models.User{
		ID: userID, Username: "software-viewer",
		Password: "existing-password-hash", IsAdmin: false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = app.DB().Delete(&models.User{}, userID).Error
	})
	token := testTokenForUser(t, "software-viewer", userID)
	router := SetupRouter()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/soft/services", nil),
		httptest.NewRequest(http.MethodGet, "/v1/soft/services/nginx/config", nil),
		httptest.NewRequest(
			http.MethodPost,
			"/v1/soft/services/nginx/config/preview",
			strings.NewReader(`{"revision":"invalid","values":{}}`),
		),
		httptest.NewRequest(
			http.MethodPost,
			"/v1/soft/services/nginx/config/apply",
			strings.NewReader(`{"revision":"invalid","values":{}}`),
		),
		httptest.NewRequest(
			http.MethodPost,
			"/v1/soft/services/nginx/actions",
			strings.NewReader(`{"action":"restart"}`),
		),
	} {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden ||
			!strings.Contains(response.Body.String(), "ADMIN_REQUIRED") {
			t.Fatalf(
				"%s %s: status=%d body=%s",
				request.Method,
				request.URL.Path,
				response.Code,
				response.Body.String(),
			)
		}
	}
}

func TestFirewallRoutesRequireAdministratorRole(t *testing.T) {
	const userID int64 = 904
	if err := app.DB().Save(&models.User{
		ID:       userID,
		Username: "firewall-viewer",
		Password: "existing-password-hash",
		IsAdmin:  false,
	}).Error; err != nil {
		t.Fatalf("save non-admin user: %v", err)
	}
	t.Cleanup(func() {
		_ = app.DB().Delete(&models.User{}, userID).Error
	})

	token := testTokenForUser(t, "firewall-viewer", userID)
	router := SetupRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/safe/info", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; response=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "ADMIN_REQUIRED") {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
}

func TestPanelUpdateRoutesRequireAdministratorRole(t *testing.T) {
	const userID int64 = 905
	if err := app.DB().Save(&models.User{
		ID: userID, Username: "update-viewer",
		Password: "existing-password-hash", IsAdmin: false,
	}).Error; err != nil {
		t.Fatalf("save non-admin user: %v", err)
	}
	t.Cleanup(func() {
		_ = app.DB().Delete(&models.User{}, userID).Error
	})
	token := testTokenForUser(t, "update-viewer", userID)
	router := SetupRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/sys/update/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; response=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "ADMIN_REQUIRED") {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
}

func TestMonitoringRoutesRequireAdministratorRole(t *testing.T) {
	const userID int64 = 908
	if err := app.DB().Save(&models.User{
		ID: userID, Username: "monitor-viewer",
		Password: "existing-password-hash", IsAdmin: false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = app.DB().Delete(&models.User{}, userID).Error
	})
	token := testTokenForUser(t, "monitor-viewer", userID)
	router := SetupRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/monitor/summary", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; response=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "ADMIN_REQUIRED") {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
}

func TestStorageRoutesAcceptAdministratorRoleRegardlessOfID(t *testing.T) {
	const userID int64 = 903
	if err := app.DB().Save(&models.User{
		ID:       userID,
		Username: "database-admin",
		Password: "existing-password-hash",
		IsAdmin:  true,
	}).Error; err != nil {
		t.Fatalf("save admin user: %v", err)
	}
	t.Cleanup(func() {
		_ = app.DB().Delete(&models.User{}, userID).Error
	})

	token := testTokenForUser(t, "database-admin", userID)
	router := SetupRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/storage/connlist?type=mysql", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code == http.StatusForbidden || recorder.Code == http.StatusUnauthorized {
		t.Fatalf("administrator was rejected: status=%d response=%s",
			recorder.Code, recorder.Body.String())
	}
}

func TestLoginUsesHttpOnlySessionCookie(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "test-only-jwt-secret-at-least-32-bytes")
	password := "R7!mQ2#vL9@xZ4"
	passwordHash, err := crypto.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	saveTestUser(t, "cookie-admin", passwordHash)

	router := SetupRouter()
	login := httptest.NewRequest(http.MethodPost, "/v1/login",
		strings.NewReader(`{"username":"cookie-admin","password":"`+password+`"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	router.ServeHTTP(loginResponse, login)

	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d; response=%s", loginResponse.Code, loginResponse.Body.String())
	}
	if strings.Contains(loginResponse.Body.String(), `"token"`) {
		t.Fatalf("login response exposed token: %s", loginResponse.Body.String())
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %d, want 1", len(cookies))
	}
	sessionCookie := cookies[0]
	if sessionCookie.Name != "oneinstack_session" || !sessionCookie.HttpOnly ||
		sessionCookie.SameSite != http.SameSiteStrictMode || sessionCookie.Path != "/v1" {
		t.Fatalf("insecure session cookie: %#v", sessionCookie)
	}

	protected := httptest.NewRequest(http.MethodGet, "/v1/sys/libcount", nil)
	protected.AddCookie(sessionCookie)
	protectedResponse := httptest.NewRecorder()
	router.ServeHTTP(protectedResponse, protected)
	if protectedResponse.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated status = %d; response=%s",
			protectedResponse.Code, protectedResponse.Body.String())
	}
}

func TestNumericUsernameCanLogin(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "test-only-jwt-secret-at-least-32-bytes")
	const (
		username = "123"
		password = "R7!mQ2#vL9@xZ4"
	)
	passwordHash, err := crypto.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	account := &models.User{
		ID: 991, Username: username, Password: passwordHash,
		IsAdmin: false, SecurityVersion: 1,
	}
	if err := app.DB().Create(account).Error; err != nil {
		t.Fatalf("create numeric username test account: %v", err)
	}
	t.Cleanup(func() {
		_ = app.DB().Unscoped().Delete(&models.User{}, account.ID).Error
	})

	router := SetupRouter()
	request := httptest.NewRequest(http.MethodPost, "/v1/login",
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("numeric username login status = %d; response=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"authenticated":true`) {
		t.Fatalf("numeric username login was not authenticated: %s", response.Body.String())
	}
}

func TestCookieAuthenticationRejectsCrossOriginMutation(t *testing.T) {
	token := testToken(t)
	router := SetupRouter()
	req := httptest.NewRequest(http.MethodPost, "/v1/logout", nil)
	req.Host = "panel.example.com"
	req.Header.Set("Origin", "https://attacker.example")
	req.AddCookie(&http.Cookie{Name: "oneinstack_session", Value: token, Path: "/v1"})
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403; response=%s",
			recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "CSRF_REJECTED") {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
}

func TestLogoutClearsSessionCookie(t *testing.T) {
	token := testToken(t)
	router := SetupRouter()
	req := httptest.NewRequest(http.MethodPost, "/v1/logout", nil)
	req.Host = "panel.example.com"
	req.Header.Set("Origin", "https://panel.example.com")
	req.AddCookie(&http.Cookie{Name: "oneinstack_session", Value: token, Path: "/v1"})
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("logout status = %d; response=%s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "oneinstack_session" || cookies[0].MaxAge >= 0 {
		t.Fatalf("logout did not clear session cookie: %#v", cookies)
	}

	reuse := httptest.NewRequest(http.MethodGet, "/v1/sys/libcount", nil)
	reuse.Header.Set("Authorization", "Bearer "+token)
	reuseResponse := httptest.NewRecorder()
	router.ServeHTTP(reuseResponse, reuse)
	if reuseResponse.Code != http.StatusUnauthorized ||
		!strings.Contains(reuseResponse.Body.String(), "SESSION_INVALIDATED") {
		t.Fatalf("logged-out session was reusable: status=%d body=%s",
			reuseResponse.Code, reuseResponse.Body.String())
	}
}

func TestAuthenticatedUserCanUpdateUsername(t *testing.T) {
	saveTestUser(t, "admin", "existing-password-hash")
	token := testToken(t)

	router := SetupRouter()
	req := httptest.NewRequest(http.MethodPost, "/v1/sys/updateuser",
		strings.NewReader(`{"username":"new-admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; response=%s",
			http.StatusOK, recorder.Code, recorder.Body.String())
	}

	var user models.User
	if err := app.DB().First(&user, 1).Error; err != nil {
		t.Fatalf("load updated user: %v", err)
	}
	if user.Username != "new-admin" {
		t.Fatalf("expected username new-admin, got %q", user.Username)
	}
}

func TestAuthenticatedUserCanResetPassword(t *testing.T) {
	currentPassword := "V8!qN3#wK7@pY5"
	currentHash, err := crypto.HashPassword(currentPassword)
	if err != nil {
		t.Fatal(err)
	}
	saveTestUser(t, "admin", currentHash)
	token := testToken(t)
	newPassword := "R7!mQ2#vL9@xZ4"

	router := SetupRouter()
	req := httptest.NewRequest(http.MethodPost, "/v1/sys/resetpassword",
		strings.NewReader(`{"currentPassword":"`+currentPassword+`","password":"`+newPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; response=%s",
			http.StatusOK, recorder.Code, recorder.Body.String())
	}

	var user models.User
	if err := app.DB().First(&user, 1).Error; err != nil {
		t.Fatalf("load updated user: %v", err)
	}
	if !crypto.CheckPasswordHash(newPassword, user.Password) {
		t.Fatal("stored password does not match the requested password")
	}
	reuse := httptest.NewRequest(http.MethodGet, "/v1/sys/libcount", nil)
	reuse.Header.Set("Authorization", "Bearer "+token)
	reuseResponse := httptest.NewRecorder()
	router.ServeHTTP(reuseResponse, reuse)
	if reuseResponse.Code != http.StatusUnauthorized {
		t.Fatalf("old password session status = %d, want 401; response=%s",
			reuseResponse.Code, reuseResponse.Body.String())
	}
}

func TestInitialAdministratorMustChangePasswordBeforeUsingPanel(t *testing.T) {
	initialPassword := "V8!qN3#wK7@pY5"
	hash, err := crypto.HashPassword(initialPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Save(&models.User{
		ID: 1, Username: "bootstrap-admin", Password: hash, IsAdmin: true,
		MustChangePassword: true, SecurityVersion: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	token := testTokenForUser(t, "bootstrap-admin", 1)
	router := SetupRouter()

	blocked := httptest.NewRequest(http.MethodGet, "/v1/sys/libcount", nil)
	blocked.Header.Set("Authorization", "Bearer "+token)
	blockedResponse := httptest.NewRecorder()
	router.ServeHTTP(blockedResponse, blocked)
	if blockedResponse.Code != http.StatusForbidden ||
		!strings.Contains(blockedResponse.Body.String(), "PASSWORD_CHANGE_REQUIRED") {
		t.Fatalf("initial session was not restricted: status=%d body=%s",
			blockedResponse.Code, blockedResponse.Body.String())
	}

	newPassword := "R7!mQ2#vL9@xZ4"
	change := httptest.NewRequest(http.MethodPost, "/v1/sys/resetpassword",
		strings.NewReader(`{"password":"`+newPassword+`"}`))
	change.Header.Set("Content-Type", "application/json")
	change.Header.Set("Authorization", "Bearer "+token)
	changeResponse := httptest.NewRecorder()
	router.ServeHTTP(changeResponse, change)
	if changeResponse.Code != http.StatusOK {
		t.Fatalf("initial password change: status=%d body=%s",
			changeResponse.Code, changeResponse.Body.String())
	}
	var account models.User
	if err := app.DB().First(&account, 1).Error; err != nil {
		t.Fatal(err)
	}
	if account.MustChangePassword || !crypto.CheckPasswordHash(newPassword, account.Password) {
		t.Fatalf("initial password state was not cleared: %#v", account)
	}
}

func TestSystemInfoDoesNotExposePassword(t *testing.T) {
	const passwordHash = "sensitive-password-hash"
	saveTestUser(t, "admin", passwordHash)
	token := testToken(t)

	router := SetupRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/sys/systeminfo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; response=%s",
			http.StatusOK, recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, passwordHash) || strings.Contains(body, `"password"`) {
		t.Fatalf("system info exposed password data: %s", body)
	}
}

func TestAuditRoutesRequireAdminAndExposeVerifiedEvents(t *testing.T) {
	if err := app.DB().Exec("DELETE FROM audit_events").Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Exec("DELETE FROM audit_checkpoints").Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Exec("DELETE FROM audit_chain_states").Error; err != nil {
		t.Fatal(err)
	}
	manager, err := auditservice.ConfigureDefault(app.DB(), bytes.Repeat([]byte{0x71}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auditservice.ClearDefault(manager) })
	if _, err := manager.Append(auditservice.EventInput{
		RequestID: "router-audit", EventType: "system", Action: "test.seed",
		Status: 200, Outcome: "success",
	}); err != nil {
		t.Fatal(err)
	}

	const adminID int64 = 906
	if err := app.DB().Save(&models.User{
		ID: adminID, Username: "audit-admin", Password: "existing-password-hash", IsAdmin: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	const viewerID int64 = 907
	if err := app.DB().Save(&models.User{
		ID: viewerID, Username: "audit-viewer", Password: "existing-password-hash", IsAdmin: false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = app.DB().Delete(&models.User{}, []int64{adminID, viewerID}).Error
	})

	router := SetupRouter()
	list := httptest.NewRequest(http.MethodGet, "/v1/audit/events?page=1&pageSize=20", nil)
	list.Header.Set("Authorization", "Bearer "+testTokenForUser(t, "audit-admin", adminID))
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), "test.seed") {
		t.Fatalf("audit list response: status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	verify := httptest.NewRequest(http.MethodPost, "/v1/audit/verify", nil)
	verify.Header.Set("Authorization", "Bearer "+testTokenForUser(t, "audit-admin", adminID))
	verifyResponse := httptest.NewRecorder()
	router.ServeHTTP(verifyResponse, verify)
	if verifyResponse.Code != http.StatusOK || !strings.Contains(verifyResponse.Body.String(), `"valid":true`) {
		t.Fatalf("audit verify response: status=%d body=%s", verifyResponse.Code, verifyResponse.Body.String())
	}

	forbidden := httptest.NewRequest(http.MethodGet, "/v1/audit/events", nil)
	forbidden.Header.Set("Authorization", "Bearer "+testTokenForUser(t, "audit-viewer", viewerID))
	forbiddenResponse := httptest.NewRecorder()
	router.ServeHTTP(forbiddenResponse, forbidden)
	if forbiddenResponse.Code != http.StatusForbidden ||
		!strings.Contains(forbiddenResponse.Body.String(), "ADMIN_REQUIRED") {
		t.Fatalf("non-admin audit response: status=%d body=%s", forbiddenResponse.Code, forbiddenResponse.Body.String())
	}
}

func TestRuntimeLogRoutesRequireAdminAndExposePersistedEntries(t *testing.T) {
	if err := app.DB().Exec("DELETE FROM runtime_log_entries").Error; err != nil {
		t.Fatal(err)
	}
	manager, err := logservice.NewRuntimeManager(app.DB(), 30, "10 5 * * *")
	if err != nil {
		t.Fatal(err)
	}
	logservice.ConfigureRuntimeDefault(manager)
	t.Cleanup(func() { logservice.ClearRuntimeDefault(manager) })
	if _, err := manager.Append(t.Context(), logservice.EntryInput{
		Level: logservice.LevelWarning, Source: "panel", Message: "router runtime marker",
	}); err != nil {
		t.Fatal(err)
	}

	const adminID int64 = 909
	const viewerID int64 = 910
	for _, account := range []models.User{
		{ID: adminID, Username: "runtime-admin", Password: "existing-password-hash", IsAdmin: true},
		{ID: viewerID, Username: "runtime-viewer", Password: "existing-password-hash", IsAdmin: false},
	} {
		if err := app.DB().Save(&account).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = app.DB().Delete(&models.User{}, []int64{adminID, viewerID}).Error
	})

	router := SetupRouter()
	list := httptest.NewRequest(http.MethodGet, "/v1/log/runtime?level=warning&q=marker", nil)
	list.Header.Set("Authorization", "Bearer "+testTokenForUser(t, "runtime-admin", adminID))
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK ||
		!strings.Contains(listResponse.Body.String(), "router runtime marker") {
		t.Fatalf("runtime log list: status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodGet, "/v1/log/runtime?afterId=invalid", nil)
	invalid.Header.Set("Authorization", "Bearer "+testTokenForUser(t, "runtime-admin", adminID))
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid runtime cursor: status=%d body=%s",
			invalidResponse.Code, invalidResponse.Body.String())
	}

	forbidden := httptest.NewRequest(http.MethodGet, "/v1/log/runtime/stats", nil)
	forbidden.Header.Set("Authorization", "Bearer "+testTokenForUser(t, "runtime-viewer", viewerID))
	forbiddenResponse := httptest.NewRecorder()
	router.ServeHTTP(forbiddenResponse, forbidden)
	if forbiddenResponse.Code != http.StatusForbidden ||
		!strings.Contains(forbiddenResponse.Body.String(), "ADMIN_REQUIRED") {
		t.Fatalf("non-admin runtime logs: status=%d body=%s",
			forbiddenResponse.Code, forbiddenResponse.Body.String())
	}
}

func TestHTTPAccessLogIsPersistedByRuntimeManager(t *testing.T) {
	if err := app.DB().Exec("DELETE FROM runtime_log_entries").Error; err != nil {
		t.Fatal(err)
	}
	manager, err := logservice.NewRuntimeManager(app.DB(), 30, "10 5 * * *")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	logservice.ConfigureRuntimeDefault(manager)
	t.Cleanup(func() { logservice.ClearRuntimeDefault(manager) })

	router := SetupRouter()
	request := httptest.NewRequest(http.MethodGet, "/v1/sys/getbaseinfo?token=must-not-appear", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", response.Code, response.Body.String())
	}
	stopContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Stop(stopContext); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Query(logservice.QueryFilter{
		Source: "http", Query: "/v1/sys/getbaseinfo", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 ||
		strings.Contains(result.Items[0].Message, "must-not-appear") {
		t.Fatalf("unexpected persisted HTTP log: %#v", result.Items)
	}
}

func testToken(t *testing.T) string {
	t.Helper()
	return testTokenForUser(t, "admin", 1)
}

func testTokenForUser(t *testing.T, username string, userID int64) string {
	t.Helper()
	t.Setenv("JWT_SECRET_KEY", "test-only-jwt-secret-at-least-32-bytes")
	var account models.User
	if err := app.DB().First(&account, userID).Error; err != nil {
		account = models.User{
			ID: userID, Username: username, Password: "test-password-hash",
			IsAdmin: userID == 1, SecurityVersion: 1,
		}
		if err := app.DB().Create(&account).Error; err != nil {
			t.Fatalf("create token user: %v", err)
		}
	}
	record, err := securityservice.NewSessionManager(app.DB()).Create(securityservice.NewSession{
		UserID: userID, Username: username, RemoteIP: "192.0.2.1",
		UserAgent: "router-test", SecurityVersion: account.EffectiveSecurityVersion(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	t.Cleanup(func() {
		_ = app.DB().Unscoped().Delete(&models.UserSession{}, "id = ?", record.ID).Error
	})
	token, _, err := utils.GenerateSessionJWT(
		username, userID, record.ID, account.EffectiveSecurityVersion(),
	)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

func saveTestUser(t *testing.T, username, password string) {
	t.Helper()
	user := &models.User{
		ID:       1,
		Username: username,
		Password: password,
		IsAdmin:  true,
	}
	if err := app.DB().Save(user).Error; err != nil {
		t.Fatalf("save test user: %v", err)
	}
}
