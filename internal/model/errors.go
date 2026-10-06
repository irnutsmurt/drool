// Package model holds canonical types shared across the controller. It
// imports nothing internal.
package model

import (
	"errors"
	"fmt"
)

// ErrorClass is a structured error category (DESIGN §12.9).
type ErrorClass string

const (
	ClassConnection     ErrorClass = "ConnectionError"
	ClassAuthentication ErrorClass = "AuthenticationError"
	ClassHostKey        ErrorClass = "HostKeyError"
	ClassAuthorization  ErrorClass = "AuthorizationError"
	ClassTimeout        ErrorClass = "TimeoutError"
	ClassExecution      ErrorClass = "ExecutionError"
	ClassConfiguration  ErrorClass = "ConfigurationError"
	ClassValidation     ErrorClass = "ValidationError"
	ClassPackageManager ErrorClass = "PackageManagerError"
	ClassService        ErrorClass = "ServiceError"
	ClassCancelled      ErrorClass = "CancelledError"
	ClassLock           ErrorClass = "LockTimeoutError"
	ClassInternal       ErrorClass = "InternalError"
)

// OpError is an error with a class, whether retrying may help, and an
// optional hint for the user.
type OpError struct {
	Class     ErrorClass
	Message   string
	Retryable bool
	Hint      string
	Err       error
}

func (e *OpError) Error() string {
	if e.Err != nil && e.Message == "" {
		return string(e.Class) + ": " + e.Err.Error()
	}
	return string(e.Class) + ": " + e.Message
}

func (e *OpError) Unwrap() error { return e.Err }

// Errorf builds an OpError.
func Errorf(class ErrorClass, retryable bool, format string, args ...any) *OpError {
	return &OpError{Class: class, Retryable: retryable, Message: fmt.Sprintf(format, args...)}
}

// ClassOf returns err's class, or ClassInternal for unclassified errors.
func ClassOf(err error) ErrorClass {
	var oe *OpError
	if errors.As(err, &oe) {
		return oe.Class
	}
	return ClassInternal
}

// AsOpError returns err as an *OpError, wrapping unclassified errors as internal.
func AsOpError(err error) *OpError {
	var oe *OpError
	if errors.As(err, &oe) {
		return oe
	}
	return &OpError{Class: ClassInternal, Message: err.Error(), Err: err}
}
