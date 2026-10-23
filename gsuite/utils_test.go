package gsuite

import (
	"testing"

	"google.golang.org/api/googleapi"
)

// DO-6422: a transient 403 from Google's Admin SDK on group-alias mutations
// (right after group creation) must be retried, not fail immediately.
func TestRetryNotFoundOrForbiddenRetries403(t *testing.T) {
	attempts := 0
	err := retryNotFoundOrForbidden(func() error {
		attempts++
		if attempts < 3 {
			return &googleapi.Error{Code: 403, Message: "Not Authorized to access this resource/api, forbidden"}
		}
		return nil
	}, 1)

	if err != nil {
		t.Fatalf("expected eventual success after retries, got error: %s", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts (2 retries + success), got %d", attempts)
	}
}

// Plain retry() must NOT retry on 403 (unchanged pre-existing behavior for
// every other resource type in this provider) — only the new
// retryNotFoundOrForbidden variant should.
func TestRetryDoesNotRetry403(t *testing.T) {
	attempts := 0
	err := retry(func() error {
		attempts++
		return &googleapi.Error{Code: 403, Message: "Not Authorized to access this resource/api, forbidden"}
	}, 1)

	if err == nil {
		t.Fatalf("expected retry() to surface the 403 as a non-retryable error")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry), got %d", attempts)
	}
}

// retryNotFoundOrForbidden must still retry on 404 too, same as retryNotFound.
func TestRetryNotFoundOrForbiddenRetries404(t *testing.T) {
	attempts := 0
	err := retryNotFoundOrForbidden(func() error {
		attempts++
		if attempts < 2 {
			return &googleapi.Error{Code: 404, Message: "Resource Not Found, notFound"}
		}
		return nil
	}, 1)

	if err != nil {
		t.Fatalf("expected eventual success after retry, got error: %s", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts (1 retry + success), got %d", attempts)
	}
}

func TestValidateEmail(t *testing.T) {

	testCases := []struct {
		email   string
		success bool
	}{
		{"myemail@domain.com", true},
		{"Alice <alice@example.com>", false},
		{"much-much-much-much-much-much-much-much-much-much-much-much-too-long@domain.com", false},
		{"\"some@much-much-much-much-much-much-much-much-much-much-much-much-too-long\"@domain.com", false},
		{"not an email address", false},
	}

	for _, testCase := range testCases {
		_, errs := validateEmail(testCase.email, "")
		if len(errs) > 0 && testCase.success {
			t.Log(errs)
			t.Errorf("expected a valid email for %s", testCase.email)
		} else if len(errs) == 0 && !testCase.success {
			t.Errorf("expected an invalid email for %s", testCase.email)
		}
	}
}
