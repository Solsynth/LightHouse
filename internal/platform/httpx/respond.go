package httpx

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"src.solsynth.dev/sosys/go/pkg/errs"
)

// Page is the fleet's pagination request: offset/take with a per-route cap.
// The response body stays a bare array and the total count travels in the
// X-Total header, exactly as the C# controllers do.
type Page struct {
	Offset int
	Take   int
}

// PageQuery parses offset/take. take above maxTake is rejected with the C#
// PAGINATION_TAKE_EXCEEDED validation error.
func PageQuery(c *gin.Context, maxTake int) (Page, bool) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	take, err := strconv.Atoi(c.DefaultQuery("take", "20"))
	if err != nil || take <= 0 {
		take = 20
	}
	if maxTake <= 0 {
		maxTake = 100
	}
	if take > maxTake {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"take": {"The 'take' parameter (" + strconv.Itoa(take) + ") exceeds the maximum allowed value (" + strconv.Itoa(maxTake) + ")."},
		}))
		return Page{}, false
	}
	return Page{Offset: offset, Take: take}, true
}

// List writes a page of items with the X-Total header.
func List[T any](c *gin.Context, items []T, total int64) {
	if items == nil {
		items = []T{}
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	c.JSON(http.StatusOK, items)
}

// OK writes 200 with body.
func OK(c *gin.Context, body any) { c.JSON(http.StatusOK, body) }

// Created writes 201 with body.
func Created(c *gin.Context, body any) { c.JSON(http.StatusCreated, body) }

// NoContent writes 200 with an empty object, which is what the C# controllers
// return for "OK" actions that carry no payload.
func NoContent(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) }

// Fail writes an ApiError.
func Fail(c *gin.Context, apiErr *errs.ApiError) {
	if apiErr.TraceId == nil {
		if trace := TraceID(c); trace != "" {
			apiErr.TraceId = &trace
		}
	}
	c.JSON(apiErr.Status, apiErr)
}

// Abort writes an ApiError and stops the chain.
func Abort(c *gin.Context, apiErr *errs.ApiError) {
	Fail(c, apiErr)
	c.Abort()
}

// BadRequest is the shorthand for a 400 with a code.
func BadRequest(c *gin.Context, code, message string) {
	Fail(c, errs.BadRequest(code, message))
}

// NotFound is the shorthand for a 404.
func NotFound(c *gin.Context, message string) {
	Fail(c, errs.NotFound(message))
}

// Unauthorized is the shorthand for a 401.
func Unauthorized(c *gin.Context) {
	Fail(c, errs.Unauthorized(""))
}

// Forbidden is the shorthand for a 403.
func Forbidden(c *gin.Context, message string) {
	Fail(c, errs.Forbidden(message))
}

// Internal is the shorthand for an unhandled 500.
func Internal(c *gin.Context) {
	Fail(c, errs.New("INTERNAL_ERROR", "An unexpected error occurred.", http.StatusInternalServerError))
}

// BindJSON decodes the body, writing the C# validation envelope on failure.
func BindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		Fail(c, errs.Validation(map[string][]string{"": {err.Error()}}))
		return false
	}
	return true
}
