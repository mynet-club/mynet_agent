package errors

import (
	"fmt"
	"testing"
)

func TestAppErrorWrap(t *testing.T) {
	base := fmt.Errorf("boom")
	e := Wrap(base, ErrCodeNetworkError, "Network error").WithDetails("timeout")
	if e.Code != ErrCodeNetworkError {
		t.Fatalf("code=%d", e.Code)
	}
	if e.Error() != "Network error: timeout" {
		t.Fatalf("msg=%q", e.Error())
	}
	if !IsAppError(e) {
		t.Fatal("IsAppError")
	}
	if ErrorCode(e) != ErrCodeNetworkError {
		t.Fatal("ErrorCode")
	}
	if e.Unwrap() != base {
		t.Fatal("Unwrap")
	}
}

func TestGetAppErrorPromotesPlain(t *testing.T) {
	e := GetAppError(fmt.Errorf("plain"))
	if e.Code != ErrCodeGeneral {
		t.Fatalf("code=%d", e.Code)
	}
}
