package oauthappctl

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/go/pkg/errs"
)

// Error codes are prefixed OAUTH_APP_ and extend the error families
// registered in wellknownctl/errorcodes.go.

// customAppStatusProduction mirrors CustomAppStatus.Production, the status
// whose redirect URIs the OIDC provider enforces.
const customAppStatusProduction = 2

var (
	errNotFound        = errors.New("oauth app not found")
	errSecretNotFound  = errors.New("oauth app secret not found")
	errSlugTaken       = errors.New("slug is already taken")
	errSlugRequired    = errors.New("slug is required")
	errNameRequired    = errors.New("name is required")
	errScopesRequired  = errors.New("allowed scopes are required")
	errRedirectMissing = errors.New("redirectUris are required for a production client")
	errOwnerRequired   = errors.New("an app needs an owner")
	errOwnerAmbiguous  = errors.New("ownership cannot be changed after creation")
	errRequestInvalid  = errors.New("invalid request")
	// errArtUnavailable covers both a deployment without a Files provider
	// and a failed file lookup: either way the requested art cannot be set.
	errArtUnavailable = errors.New("app art is unavailable")
	errForbidden      = errors.New("you do not have permission to manage this app")
	errUnauthorized   = errors.New("authentication is required")
)

func respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errNotFound):
		jsonError(c, http.StatusNotFound, "OAUTH_APP_NOT_FOUND", "OAuth app was not found.")
	case errors.Is(err, errSecretNotFound):
		jsonError(c, http.StatusNotFound, "OAUTH_APP_SECRET_NOT_FOUND", "Secret was not found.")
	case errors.Is(err, errSlugTaken):
		jsonError(c, http.StatusConflict, "OAUTH_APP_SLUG_TAKEN", "The slug is already taken.")
	case errors.Is(err, errSlugRequired), errors.Is(err, errNameRequired):
		jsonError(c, http.StatusBadRequest, "OAUTH_APP_NAME_SLUG_REQUIRED", "Name and slug are required.")
	case errors.Is(err, errScopesRequired):
		jsonError(c, http.StatusBadRequest, "OAUTH_APP_SCOPES_REQUIRED", "At least one allowed scope is required.")
	case errors.Is(err, errRedirectMissing):
		jsonError(c, http.StatusBadRequest, "OAUTH_APP_REDIRECT_URIS_REQUIRED", "Redirect URIs are required for a production client.")
	case errors.Is(err, errOwnerRequired), errors.Is(err, errOwnerAmbiguous):
		jsonError(c, http.StatusBadRequest, "OAUTH_APP_OWNER_INVALID", "An app is owned by a project or by an account, and ownership cannot be changed.")
	case errors.Is(err, errArtUnavailable):
		jsonError(c, http.StatusBadRequest, "OAUTH_APP_ART_UNAVAILABLE", "App art cannot be resolved in this deployment.")
	case errors.Is(err, errForbidden):
		jsonError(c, http.StatusForbidden, "OAUTH_APP_FORBIDDEN", "You do not have permission to manage this app.")
	case errors.Is(err, errUnauthorized):
		c.Status(http.StatusUnauthorized)
	default:
		jsonError(c, http.StatusBadRequest, "OAUTH_APP_REQUEST_INVALID", err.Error())
	}
}

func jsonError(c *gin.Context, status int, code, message string) {
	c.JSON(status, errs.New(code, message, status))
}
