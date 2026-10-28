package fediverse

import (
	"net/http"

	"github.com/google/uuid"
	"src.solsynth.dev/sosys/go/pkg/errs"
)

// Small ApiError constructors shared by the handlers. They keep the C# error
// codes at the call sites instead of repeating errs.New everywhere.
func errsNotFound(code, message string) *errs.ApiError {
	return errs.New(code, message, http.StatusNotFound)
}

func errsActorNotFound() *errs.ApiError {
	return errsNotFound("ACTIVITYPUB_ACTOR_NOT_FOUND", "Actor not found")
}

func errsRuleNotFound() *errs.ApiError {
	return errsNotFound("FEDIVERSE_RULE_NOT_FOUND", "Rule not found.")
}

func errsServiceUnavailable(message string) *errs.ApiError {
	return errs.New("SERVICE_UNAVAILABLE", message, http.StatusServiceUnavailable)
}

func errsNotAcceptable(message string) *errs.ApiError {
	return errs.New("ACTIVITYPUB_INVALID_CONTENT_TYPE", message, http.StatusNotAcceptable)
}

func uuidNil() uuid.UUID { return uuid.Nil }
