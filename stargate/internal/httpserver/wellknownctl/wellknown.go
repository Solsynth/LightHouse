// Package wellknownctl ports DysonNetwork.Padlock's WellKnownController
// (GET /.well-known/permissions + GET /.well-known/error-codes) and serves
// the swagger playground manifests for the ported Padlock (auth) and
// Passport (profile) endpoint surfaces.
package wellknownctl

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
)

// Deps carries dependencies for the well-known endpoints. The manifests are
// static registry dumps, so no services are required.
type Deps struct{}

// Register wires the /api surface of WellKnownController. The C# controller
// declares no /api routes — both of its endpoints are absolute /.well-known
// paths served via RegisterTop — so this is intentionally empty. It exists to
// satisfy the standard controller registration contract.
func Register(api *gin.RouterGroup, d Deps) {}

// RegisterTop wires the top-level (non-/api) endpoints the gateway serves
// unprefixed: the WellKnownController manifests and the swagger playground
// JSON documents.
func RegisterTop(engine *gin.Engine, d Deps) {
	engine.GET("/.well-known/permissions", listPermissions)
	engine.GET("/.well-known/error-codes", listErrorCodes)
	engine.GET("/swagger/padlock/v1/swagger.json", serveSwagger(padlockSwagger))
	engine.GET("/swagger/passport/v1/swagger.json", serveSwagger(passportSwagger))
}

type permissionsResponse struct {
	Count       int              `json:"count"`
	Permissions []permissionItem `json:"permissions"`
}

// listPermissions mirrors WellKnownController.ListPermissions: every public
// static string literal of PermissionKeys.cs, serialized as {key, name} and
// ordered by key (the C# reflection query does the same OrderBy).
func listPermissions(c *gin.Context) {
	items := make([]permissionItem, len(permissionEntries))
	copy(items, permissionEntries)
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	c.JSON(http.StatusOK, permissionsResponse{Count: len(items), Permissions: items})
}

type errorCodesResponse struct {
	Count      int            `json:"count"`
	ErrorCodes errorCodesBody `json:"error_codes"`
}

type errorCodesBody struct {
	General    []errorCodeItem     `json:"general"`
	Categories []errorCodeCategory `json:"categories"`
}

// listErrorCodes mirrors WellKnownController.ListErrorCodes: the top-level
// constants of ErrorCodes.cs under "general", plus one category per nested
// static class. Both levels are sorted like the C# reflection queries
// (categories by name, codes by code). The C# Where(c => c.codes.Count > 0)
// filter is a no-op here — every category is non-empty.
func listErrorCodes(c *gin.Context) {
	general := make([]errorCodeItem, len(topLevelErrorCodes))
	copy(general, topLevelErrorCodes)
	sort.Slice(general, func(i, j int) bool { return general[i].Code < general[j].Code })

	categories := make([]errorCodeCategory, 0, len(errorCodeCategories))
	for _, cat := range errorCodeCategories {
		codes := make([]errorCodeItem, len(cat.Codes))
		copy(codes, cat.Codes)
		sort.Slice(codes, func(i, j int) bool { return codes[i].Code < codes[j].Code })
		categories = append(categories, errorCodeCategory{Category: cat.Category, Codes: codes})
	}
	sort.Slice(categories, func(i, j int) bool { return categories[i].Category < categories[j].Category })

	count := len(general)
	for _, cat := range categories {
		count += len(cat.Codes)
	}

	c.JSON(http.StatusOK, errorCodesResponse{
		Count: count,
		ErrorCodes: errorCodesBody{
			General:    general,
			Categories: categories,
		},
	})
}
