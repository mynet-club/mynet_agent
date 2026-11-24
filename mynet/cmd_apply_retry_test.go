package mynet

import (
	"testing"
	"time"
)

func TestRetryApplyWithBackoffRecoversAfterTransientTunError(t *testing.T) {
	calls := 0
	retries := 0
	rc := retryApplyWithBackoff(3, time.Millisecond, false, func() int {
		calls++
		if calls < 2 {
			return 1
		}
		return 0
	}, func(attempt, maxAttempts int, wait time.Duration) {
		retries++
	})
	if rc != 0 || calls != 2 || retries != 1 {
		t.Fatalf("got rc=%d calls=%d retries=%d, want 0, 2, 1", rc, calls, retries)
	}
}

func TestRetryApplyWithBackoffFailsLoudAfterExhaustingRetries(t *testing.T) {
	calls := 0
	retries := 0
	rc := retryApplyWithBackoff(3, time.Millisecond, false, func() int {
		calls++
		return 1
	}, func(attempt, maxAttempts int, wait time.Duration) {
		retries++
	})
	if rc != 1 || calls != 3 || retries != 2 {
		t.Fatalf("got rc=%d calls=%d retries=%d, want 1, 3, 2", rc, calls, retries)
	}
}
