package errors

import (
	"fmt"
	"runtime"
	"strings"
)

// 错误码（原 common.ErrCode*，迁入 errors 包避免与 utils 循环依赖）
const (
	ErrCodeSuccess         = 0
	ErrCodeGeneral         = 1
	ErrCodeInvalidConfig   = 2
	ErrCodeAuthFailed      = 3
	ErrCodeNetworkError    = 4
	ErrCodeFileSystemError = 5
	ErrCodeNodeError       = 6
)

// AppError 应用错误
type AppError struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Details   string `json:"details,omitempty"`
	Cause     error  `json:"-"`
	Stack     string `json:"stack,omitempty"`
	Component string `json:"component,omitempty"`
}

func (e *AppError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("%s: %s", e.Message, e.Details)
	}
	return e.Message
}

func (e *AppError) Unwrap() error { return e.Cause }

func New(code int, message string) *AppError {
	return &AppError{Code: code, Message: message, Stack: getStack()}
}

func Newf(code int, format string, args ...interface{}) *AppError {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...), Stack: getStack()}
}

func Wrap(err error, code int, message string) *AppError {
	return &AppError{Code: code, Message: message, Cause: err, Stack: getStack()}
}

func Wrapf(err error, code int, format string, args ...interface{}) *AppError {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...), Cause: err, Stack: getStack()}
}

func (e *AppError) WithComponent(component string) *AppError {
	e.Component = component
	return e
}

func (e *AppError) WithDetails(details string) *AppError {
	e.Details = details
	return e
}

func ErrInvalidConfig(details string) *AppError {
	return New(ErrCodeInvalidConfig, "Invalid configuration").WithDetails(details)
}

func ErrAuthFailed(details string) *AppError {
	return New(ErrCodeAuthFailed, "Authentication failed").WithDetails(details)
}

func ErrNetworkError(err error) *AppError {
	return Wrap(err, ErrCodeNetworkError, "Network error")
}

func ErrFileSystemError(err error, operation string) *AppError {
	return Wrap(err, ErrCodeFileSystemError, fmt.Sprintf("File system error: %s", operation))
}

func ErrNodeError(err error, operation string) *AppError {
	return Wrap(err, ErrCodeNodeError, fmt.Sprintf("Node operation failed: %s", operation))
}

func getStack() string {
	const depth = 32
	var pcs [depth]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])

	var stack []string
	for {
		frame, more := frames.Next()
		if !strings.Contains(frame.File, "runtime/") {
			stack = append(stack, fmt.Sprintf("%s:%d %s", frame.File, frame.Line, frame.Function))
		}
		if !more {
			break
		}
	}
	return strings.Join(stack, "\n")
}

func IsAppError(err error) bool {
	_, ok := err.(*AppError)
	return ok
}

func GetAppError(err error) *AppError {
	if appErr, ok := err.(*AppError); ok {
		return appErr
	}
	return Wrap(err, ErrCodeGeneral, "Unknown error")
}

func ErrorCode(err error) int {
	if appErr := GetAppError(err); appErr != nil {
		return appErr.Code
	}
	return ErrCodeGeneral
}
