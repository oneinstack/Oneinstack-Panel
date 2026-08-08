package core

import "testing"

func TestErrorResponseOmitsSyntheticDetail(t *testing.T) {
	response := ErrorResponse(NewFieldError(ErrBadRequest, "用户名格式不正确", "username"))
	if response.Message != "用户名格式不正确" {
		t.Fatalf("message = %q", response.Message)
	}
	if response.Error == nil {
		t.Fatal("expected structured error")
	}
	if response.Error.Detail != "" {
		t.Fatalf("synthetic detail must be omitted, got %q", response.Error.Detail)
	}
}
