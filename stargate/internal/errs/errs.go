// Package errs defines the canonical ApiError wire shape used across the
// Dyson Network API. Fields serialize snake_case with nulls omitted except
// traceId, which is camelCase (a documented historical exception).
package errs

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ApiError mirrors DysonNetwork.Shared.Networking.ApiError.
type ApiError struct {
	Code    string              `json:"code"`
	Message string              `json:"message"`
	Status  int                 `json:"status"`
	Detail  *string             `json:"detail,omitempty"`
	TraceId *string             `json:"traceId,omitempty"`
	Errors  map[string][]string `json:"errors,omitempty"`
	Meta    any                 `json:"meta,omitempty"`
}

func (e *ApiError) Error() string {
	return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Message)
}

// New builds an ApiError with the given code, message and HTTP status.
func New(code string, message string, status int) *ApiError {
	return &ApiError{Code: code, Message: message, Status: status}
}

// Validation builds a 400 validation error carrying per-field messages.
func Validation(errors map[string][]string) *ApiError {
	return &ApiError{
		Code:    "VALIDATION_ERROR",
		Message: "The request data is invalid.",
		Status:  http.StatusBadRequest,
		Errors:  errors,
	}
}

// NotFound builds a 404 NOT_FOUND error.
func NotFound(message string) *ApiError {
	return New("NOT_FOUND", message, http.StatusNotFound)
}

// Unauthorized builds a 401 UNAUTHORIZED error, mirroring
// ApiError.Unauthorized: an empty message falls back to the canonical
// "Authentication is required.".
func Unauthorized(message string) *ApiError {
	if message == "" {
		message = "Authentication is required."
	}
	return New("UNAUTHORIZED", message, http.StatusUnauthorized)
}

// Forbidden builds a 403 FORBIDDEN error.
func Forbidden(message string) *ApiError {
	return New("FORBIDDEN", message, http.StatusForbidden)
}

// BadRequest builds a 400 error with the given code.
func BadRequest(code, message string) *ApiError {
	return New(code, message, http.StatusBadRequest)
}

// MarshalJSON enforces snake_case for ApiError fields except traceId.
// The struct tags already encode this; this method exists as a guard and for
// future-proofing.
func (e *ApiError) MarshalJSON() ([]byte, error) {
	type alias ApiError
	return json.Marshal((*alias)(e))
}
