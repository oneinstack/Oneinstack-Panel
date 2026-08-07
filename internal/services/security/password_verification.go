package security

import (
	"fmt"
	"sync"
	"time"
)

const (
	passwordVerificationFailureLimit = 5
	passwordVerificationWindow       = 10 * time.Minute
	passwordVerificationCooldown     = 15 * time.Minute
)

type passwordVerificationAttempt struct {
	failures    []time.Time
	lockedUntil time.Time
}

type passwordVerificationGuard struct {
	mu      sync.Mutex
	entries map[string]passwordVerificationAttempt
}

var defaultPasswordVerificationGuard = &passwordVerificationGuard{
	entries: make(map[string]passwordVerificationAttempt),
}

// PasswordVerificationAllowed reports whether a user may attempt re-authentication.
// The key includes the client IP so a failed re-authentication cannot lock the
// account for every legitimate client.
func PasswordVerificationAllowed(userID int64, remoteIP string) (bool, time.Duration) {
	key := passwordVerificationKey(userID, remoteIP)
	now := time.Now()

	defaultPasswordVerificationGuard.mu.Lock()
	defer defaultPasswordVerificationGuard.mu.Unlock()

	entry, exists := defaultPasswordVerificationGuard.entries[key]
	if !exists {
		return true, 0
	}
	if entry.lockedUntil.After(now) {
		return false, time.Until(entry.lockedUntil)
	}

	entry.lockedUntil = time.Time{}
	entry.failures = recentPasswordVerificationFailures(entry.failures, now)
	if len(entry.failures) == 0 {
		delete(defaultPasswordVerificationGuard.entries, key)
	} else {
		defaultPasswordVerificationGuard.entries[key] = entry
	}
	return true, 0
}

// RecordPasswordVerificationFailure records a failed re-authentication and
// returns the cooldown duration when the failure limit has been reached.
func RecordPasswordVerificationFailure(userID int64, remoteIP string) (bool, time.Duration) {
	key := passwordVerificationKey(userID, remoteIP)
	now := time.Now()

	defaultPasswordVerificationGuard.mu.Lock()
	defer defaultPasswordVerificationGuard.mu.Unlock()

	entry := defaultPasswordVerificationGuard.entries[key]
	entry.failures = recentPasswordVerificationFailures(entry.failures, now)
	entry.failures = append(entry.failures, now)
	if len(entry.failures) >= passwordVerificationFailureLimit {
		entry.failures = nil
		entry.lockedUntil = now.Add(passwordVerificationCooldown)
		defaultPasswordVerificationGuard.entries[key] = entry
		return true, passwordVerificationCooldown
	}
	defaultPasswordVerificationGuard.entries[key] = entry
	return false, 0
}

// ResetPasswordVerificationFailures clears the failure counter after a
// successful re-authentication.
func ResetPasswordVerificationFailures(userID int64, remoteIP string) {
	key := passwordVerificationKey(userID, remoteIP)
	defaultPasswordVerificationGuard.mu.Lock()
	delete(defaultPasswordVerificationGuard.entries, key)
	defaultPasswordVerificationGuard.mu.Unlock()
}

func passwordVerificationKey(userID int64, remoteIP string) string {
	return fmt.Sprintf("%d:%s", userID, remoteIP)
}

func recentPasswordVerificationFailures(failures []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-passwordVerificationWindow)
	recent := failures[:0]
	for _, failure := range failures {
		if failure.After(cutoff) {
			recent = append(recent, failure)
		}
	}
	return recent
}
