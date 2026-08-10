package utils

import "testing"

func TestValidateUsernameAcceptsNumericAccount(t *testing.T) {
	if err := ValidateUsername("123"); err != nil {
		t.Fatalf("numeric username should be valid: %v", err)
	}
}

func TestValidateUsernameRejectsUnsupportedCharacters(t *testing.T) {
	if err := ValidateUsername("user@example.com"); err == nil {
		t.Fatal("username containing unsupported characters should be rejected")
	}
}
