// Package passkitctl ports Passport's PassKitController HTTP surface and the
// account member-pass download endpoint.
package passkitctl

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/passkit"
)

// Deps is the dependency set the PassKit controller needs.
type Deps struct {
	Service *passkit.Service
	Log     *slog.Logger
}

// Register mounts /api/passkit/v1 plus the authenticated
// GET /api/accounts/me/passbook/member download.
func Register(api *gin.RouterGroup, d Deps) {
	group := api.Group("/passkit/v1")
	group.GET("", d.root)
	group.POST("/devices/:deviceLibraryIdentifier/registrations/:passTypeIdentifier/:serialNumber", d.registerDevice)
	group.DELETE("/devices/:deviceLibraryIdentifier/registrations/:passTypeIdentifier/:serialNumber", d.unregisterDevice)
	group.GET("/devices/:deviceLibraryIdentifier/registrations/:passTypeIdentifier", d.updatedSerialNumbers)
	group.GET("/passes/:passTypeIdentifier/:serialNumber", d.latestPass)
	group.POST("/log", d.logEntries)

	api.Group("/accounts/me").GET("/passbook/member", middleware.RequireAuth(), d.memberPass)
}

func (d Deps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func (d Deps) root(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"service": "passkit",
		"version": "v1",
		"message": "Apple Wallet PassKit service endpoint.",
		"endpoints": []string{
			"POST devices/{deviceLibraryIdentifier}/registrations/{passTypeIdentifier}/{serialNumber}",
			"DELETE devices/{deviceLibraryIdentifier}/registrations/{passTypeIdentifier}/{serialNumber}",
			"GET devices/{deviceLibraryIdentifier}/registrations/{passTypeIdentifier}",
			"GET passes/{passTypeIdentifier}/{serialNumber}",
			"POST log",
		},
	})
}

type registrationRequest struct {
	PushToken string `json:"pushToken"`
}

func (d Deps) registerDevice(c *gin.Context) {
	pass, ok := d.authorizedPass(c)
	if !ok {
		return
	}
	var request registrationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"PushToken": {"Push token is required."},
		}))
		return
	}
	if strings.TrimSpace(request.PushToken) == "" {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"PushToken": {"Push token is required."},
		}))
		return
	}
	if err := d.Service.RegisterDevice(c.Request.Context(), pass,
		c.Param("deviceLibraryIdentifier"), request.PushToken); err != nil {
		d.serviceError(c, err)
		return
	}
	c.Status(http.StatusCreated)
}

func (d Deps) unregisterDevice(c *gin.Context) {
	pass, ok := d.authorizedPass(c)
	if !ok {
		return
	}
	if _, err := d.Service.UnregisterDevice(c.Request.Context(), pass, c.Param("deviceLibraryIdentifier")); err != nil {
		d.serviceError(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// serialNumbersResponse is PassKitController.PassSerialNumbersResponse.
type serialNumbersResponse struct {
	SerialNumbers []string `json:"serialNumbers"`
	LastUpdated   string   `json:"lastUpdated"`
}

func (d Deps) updatedSerialNumbers(c *gin.Context) {
	passTypeIdentifier := c.Param("passTypeIdentifier")
	serials, lastUpdated, err := d.Service.GetUpdatedSerialNumbers(
		c.Request.Context(),
		c.Param("deviceLibraryIdentifier"),
		passTypeIdentifier,
		c.Query("passesUpdatedSince"),
	)
	if err != nil {
		d.serviceError(c, err)
		return
	}
	if len(serials) == 0 {
		c.Status(http.StatusNoContent)
		return
	}

	first, err := d.Service.GetPassBySerial(c.Request.Context(), passTypeIdentifier, serials[0])
	if err != nil {
		d.serviceError(c, err)
		return
	}
	if first == nil || !d.Service.IsAuthorized(first, c.GetHeader("Authorization")) {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	c.JSON(http.StatusOK, serialNumbersResponse{SerialNumbers: serials, LastUpdated: lastUpdated})
}

func (d Deps) latestPass(c *gin.Context) {
	pass, ok := d.authorizedPass(c)
	if !ok {
		return
	}
	archive, updated, err := d.Service.GenerateMemberPass(c.Request.Context(), pass.AccountID)
	if err != nil {
		d.serviceError(c, err)
		return
	}
	d.writePass(c, archive, updated)
}

func (d Deps) memberPass(c *gin.Context) {
	user := middleware.CurrentUser(c.Request.Context())
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	accountID, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	archive, pass, err := d.Service.GenerateMemberPass(c.Request.Context(), accountID)
	if err != nil {
		d.serviceError(c, err)
		return
	}
	d.writePass(c, archive, pass)
}

func (d Deps) writePass(c *gin.Context, archive []byte, pass *passkit.PassEntity) {
	if pass != nil {
		c.Header("ETag", "\""+pass.LastUpdatedTag+"\"")
	}
	c.Header("Content-Disposition", `attachment; filename="member.pkpass"`)
	c.Data(http.StatusOK, "application/vnd.apple.pkpass", archive)
}

type passLogRequest struct {
	Logs []string `json:"logs"`
}

func (d Deps) logEntries(c *gin.Context) {
	// This sink is anonymous by Apple's protocol. Cap the body and the entry
	// count so a caller cannot exhaust memory or the log volume.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var request passLogRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{
			"logs": {"The request body is invalid."},
		}))
		return
	}
	if len(request.Logs) > 20 {
		request.Logs = request.Logs[:20]
	}
	for _, line := range request.Logs {
		if len(line) > 1024 {
			line = line[:1024]
		}
		d.log().Info("Apple Wallet log: " + line)
	}
	c.Status(http.StatusOK)
}

// authorizedPass loads the pass named by the route and verifies the
// `ApplePass <token>` header. A missing pass and a bad token both answer 401,
// mirroring RequireAuthorizedPassAsync.
func (d Deps) authorizedPass(c *gin.Context) (*passkit.PassEntity, bool) {
	pass, err := d.Service.GetPassBySerial(c.Request.Context(), c.Param("passTypeIdentifier"), c.Param("serialNumber"))
	if err != nil {
		d.serviceError(c, err)
		return nil, false
	}
	if pass == nil || !d.Service.IsAuthorized(pass, c.GetHeader("Authorization")) {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return nil, false
	}
	return pass, true
}

func (d Deps) serviceError(c *gin.Context, err error) {
	var configErr *passkit.ConfigError
	switch {
	case errors.As(err, &configErr):
		c.JSON(http.StatusServiceUnavailable, errs.New("SERVICE_UNAVAILABLE", configErr.Error(), http.StatusServiceUnavailable))
	case errors.Is(err, passkit.ErrAccountNotFound):
		c.JSON(http.StatusNotFound, errs.NotFound("Account was not found."))
	default:
		d.log().Error("passkit request failed", "error", err)
		c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
	}
}
