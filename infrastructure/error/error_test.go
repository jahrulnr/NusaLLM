package apperror

import (
	"errors"
	"fmt"
	"testing"
)

func TestAsUnwrapsAppError(t *testing.T) {
	err := fmt.Errorf("load: %w", NewNotFoundError("gateway", errors.New("no file")))
	got := As(err)
	if got.StatusCode() != StatusNotFound || got.Error() != "gateway not found" {
		t.Fatalf("As = %d %q", got.StatusCode(), got.Error())
	}
}

func TestAsFallbackHidesCause(t *testing.T) {
	got := As(errors.New("sql: password=abc leaked"))
	if got.StatusCode() != StatusInternalServerError || got.Error() != "internal error" {
		t.Fatalf("fallback leaked: %q", got.Error())
	}
	if got.Unwrap() == nil {
		t.Fatal("fallback should still wrap the cause")
	}
}

func TestFromStatus(t *testing.T) {
	e := FromStatus(429, "rate limited", errors.New("retry after 30s"))
	if e.StatusCode() != 429 || e.Error() != "rate limited" {
		t.Fatalf("FromStatus = %d %q", e.StatusCode(), e.Error())
	}
	if e := FromStatus(200, "weird", nil); e.StatusCode() != StatusBadGateway {
		t.Fatal("non-error status should map to 502")
	}
}

func TestInvalidCodeFallsBack(t *testing.T) {
	e := New(42, "x", nil)
	if e.StatusCode() != StatusInternalServerError {
		t.Fatal("invalid code should fall back to 500")
	}
}
