// Package nfcctl ports Passport's NfcController and NfcAdminController HTTP
// surface: the anonymous scan/lookup routes, the authenticated tag-management
// routes and the admin factory routes.
package nfcctl

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/nfc"
	"src.solsynth.dev/sosys/stargate/permission"
)

// Deps is the dependency set the NFC controllers need.
type Deps struct {
	Service *nfc.Service
	Perm    *permission.Service
	Log     *slog.Logger
}

// Register mounts /api/nfc.
func Register(api *gin.RouterGroup, d Deps) {
	group := api.Group("/nfc")
	group.GET("", d.resolve)
	group.GET("/lookup", d.lookup)
	group.GET("/tags", middleware.RequireAuth(), d.listTags)
	group.GET("/tags/:id", d.getByID)
	group.POST("/tags", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.NfcAdminManage), d.registerTag)
	group.POST("/tags/claim", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.NfcTagsClaim), d.claimTag)
	group.PATCH("/tags/:tagId", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.NfcTagsUpdate), d.updateTag)
	group.POST("/tags/:tagId/lock", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.NfcTagsLock), d.lockTag)
	group.DELETE("/tags/:tagId", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.NfcTagsDelete), d.unregisterTag)
}

// RegisterAdmin mounts /api/admin/nfc.
func RegisterAdmin(api *gin.RouterGroup, d Deps) {
	group := api.Group("/admin/nfc")
	group.GET("/tags", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.NfcAdminManage), d.listEncryptedTags)
	group.POST("/tags", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.NfcAdminManage), d.createEncryptedTag)
}

func (d Deps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func currentUser(c *gin.Context) *model.Account {
	return middleware.CurrentUser(c.Request.Context())
}

func observerID(c *gin.Context) *uuid.UUID {
	user := currentUser(c)
	if user == nil {
		return nil
	}
	id, err := uuid.Parse(user.Id)
	if err != nil {
		return nil
	}
	return &id
}

func internalError(c *gin.Context, err error, log *slog.Logger) {
	log.Error("nfc request failed", "error", err)
	c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
}

func nfcNotFound(message string) *errs.ApiError {
	resource := "nfc_tag"
	return &errs.ApiError{
		Code:    "NOT_FOUND",
		Message: message,
		Status:  http.StatusNotFound,
		Detail:  &resource,
	}
}

func validationError(field, message string) *errs.ApiError {
	return errs.Validation(map[string][]string{field: {message}})
}

// resolve handles GET /api/nfc: resolve a tag to an account. Anonymous
// callers are allowed; an authenticated caller gets relationship metadata.
func (d Deps) resolve(c *gin.Context) {
	observer := observerID(c)
	ctx := c.Request.Context()

	if piccData := c.Query("picc_data"); strings.TrimSpace(piccData) != "" {
		result, err := d.Service.ValidateSun(ctx, piccData, c.Query("e"), c.Query("cmac"), observer)
		d.respondValidation(c, result, err)
		return
	}

	uid := c.Query("uid")
	if strings.HasPrefix(strings.ToLower(uid), "solian://") {
		piccData, encData, cmac, ok := nfc.ParseSolianURL(uid)
		if !ok {
			c.JSON(http.StatusBadRequest, validationError("uid",
				"Invalid solian:// URL format. Expected: solian://phpass?picc_data=...&e=...&cmac=..."))
			return
		}
		result, err := d.Service.ValidateSun(ctx, piccData, encData, cmac, observer)
		d.respondValidation(c, result, err)
		return
	}

	if strings.TrimSpace(uid) == "" {
		c.JSON(http.StatusBadRequest, validationError("uid", "Parameter 'uid' or 'picc_data' is required."))
		return
	}

	// A 32+ character hex uid is the legacy encrypted format; Passport no
	// longer supports it and falls through to the plain UID lookup.
	result, err := d.Service.Resolve(ctx, uid, observer)
	if err != nil {
		internalError(c, err, d.log())
		return
	}
	if result == nil {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}
	c.JSON(http.StatusOK, resolveResponse(result))
}

func resolveResponse(result *nfc.ResolveResult) nfc.ResolveResponse {
	return nfc.ResolveResponse{
		ID:        result.ID.String(),
		Account:   result.Account,
		IsFriend:  result.IsFriend,
		IsClaimed: result.IsClaimed,
		Actions:   result.Actions,
	}
}

func (d Deps) respondValidation(c *gin.Context, result *nfc.ValidationResult, err error) {
	switch {
	case errors.Is(err, nfc.ErrReplay):
		c.JSON(http.StatusBadRequest, validationError("counter", err.Error()))
		return
	case err != nil:
		internalError(c, err, d.log())
		return
	case result == nil:
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found or invalid CMAC."))
		return
	}

	switch result.ClaimStatus {
	case nfc.ClaimNeedsAuth:
		c.JSON(http.StatusForbidden, errs.New("PASSPORT_NFC_TAG_UNCLAIMED",
			"This tag is not yet associated with an account. Please sign in to claim it.", http.StatusForbidden))
		return
	case nfc.ClaimUnclaimed:
		c.JSON(http.StatusOK, nfc.ResolveResponse{
			ID:        result.Tag.ID.String(),
			Account:   nil,
			IsFriend:  false,
			IsClaimed: false,
			Actions:   result.Actions,
		})
		return
	case nfc.ClaimPreAssignedMismatch:
		c.JSON(http.StatusForbidden, errs.New("PASSPORT_NFC_TAG_PRE_ASSIGNED",
			"This tag is assigned to a different account.", http.StatusForbidden))
		return
	}

	if result.Account == nil {
		c.JSON(http.StatusNotFound, nfcNotFound("Tag owner not found."))
		return
	}
	c.JSON(http.StatusOK, nfc.ResolveResponse{
		ID:        result.Tag.ID.String(),
		Account:   result.Account,
		IsFriend:  result.IsFriend,
		IsClaimed: result.IsClaimed,
		Actions:   result.Actions,
	})
}

// lookup handles GET /api/nfc/lookup (admin/debug).
func (d Deps) lookup(c *gin.Context) {
	uid := c.Query("uid")
	if strings.TrimSpace(uid) == "" {
		c.JSON(http.StatusBadRequest, validationError("uid", "Parameter 'uid' is required."))
		return
	}
	result, err := d.Service.LookupByUID(c.Request.Context(), uid, observerID(c))
	if err != nil {
		internalError(c, err, d.log())
		return
	}
	if result == nil {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}
	c.JSON(http.StatusOK, resolveResponse(result))
}

// getByID handles GET /api/nfc/tags/:id.
func (d Deps) getByID(c *gin.Context) {
	tagID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}
	result, err := d.Service.LookupByID(c.Request.Context(), tagID, observerID(c))
	if err != nil {
		internalError(c, err, d.log())
		return
	}
	if result == nil {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}
	c.JSON(http.StatusOK, resolveResponse(result))
}

// listTags handles GET /api/nfc/tags.
func (d Deps) listTags(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	userID, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	tags, err := d.Service.ListTags(c.Request.Context(), userID)
	if err != nil {
		internalError(c, err, d.log())
		return
	}
	response := make([]nfc.TagResponse, 0, len(tags))
	for i := range tags {
		response = append(response, nfc.NewTagResponse(&tags[i]))
	}
	c.JSON(http.StatusOK, response)
}

type registerTagRequest struct {
	UID   string  `json:"uid"`
	Label *string `json:"label"`
}

// registerTag handles POST /api/nfc/tags (admin-issued plain tags).
func (d Deps) registerTag(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	userID, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}

	var request registerTagRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, validationError("uid", "The request body is invalid."))
		return
	}
	if strings.TrimSpace(request.UID) == "" || len(request.UID) > 64 {
		c.JSON(http.StatusBadRequest, validationError("uid", "The Uid field is required."))
		return
	}
	if request.Label != nil && len(*request.Label) > 64 {
		c.JSON(http.StatusBadRequest, validationError("label", "The field Label must be a string with a maximum length of 64."))
		return
	}

	tag, err := d.Service.RegisterTag(c.Request.Context(), userID, request.UID, request.Label)
	if err != nil {
		if errors.Is(err, nfc.ErrTagExists) {
			c.JSON(http.StatusConflict, errs.New("PASSPORT_NFC_TAG_EXISTS", err.Error(), http.StatusConflict))
			return
		}
		internalError(c, err, d.log())
		return
	}
	c.JSON(http.StatusOK, nfc.NewTagResponse(tag))
}

type claimTagRequest struct {
	UID      *string    `json:"uid"`
	RecordID *uuid.UUID `json:"recordId"`
}

// claimTag handles POST /api/nfc/tags/claim.
func (d Deps) claimTag(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	userID, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}

	var request claimTagRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, validationError("uid", "The request body is invalid."))
		return
	}
	if request.UID == nil && request.RecordID == nil {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_NFC_CLAIM_ID_REQUIRED",
			"Uid or RecordId must be provided.", http.StatusBadRequest))
		return
	}

	ctx := c.Request.Context()
	var tag *nfc.TagEntity
	claimErr := error(nil)
	if request.RecordID != nil {
		tag, claimErr = d.Service.ClaimTagByID(ctx, *request.RecordID, userID)
	} else {
		tag, claimErr = d.Service.ClaimTagByUID(ctx, *request.UID, userID)
	}
	if claimErr != nil {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_NFC_TAG_CLAIM_FAILED", claimErr.Error(), http.StatusBadRequest))
		return
	}
	c.JSON(http.StatusOK, nfc.NewTagResponse(tag))
}

type updateTagRequest struct {
	Label    *string `json:"label"`
	IsActive *bool   `json:"isActive"`
}

// updateTag handles PATCH /api/nfc/tags/:tagId.
func (d Deps) updateTag(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	userID, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	tagID, err := uuid.Parse(c.Param("tagId"))
	if err != nil {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}

	var request updateTagRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, validationError("label", "The request body is invalid."))
		return
	}
	if request.Label != nil && len(*request.Label) > 64 {
		c.JSON(http.StatusBadRequest, validationError("label", "The field Label must be a string with a maximum length of 64."))
		return
	}

	tag, err := d.Service.UpdateTag(c.Request.Context(), userID, tagID, request.Label, request.IsActive)
	if err != nil {
		internalError(c, err, d.log())
		return
	}
	if tag == nil {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}
	c.JSON(http.StatusOK, nfc.NewTagResponse(tag))
}

// lockTag handles POST /api/nfc/tags/:tagId/lock.
func (d Deps) lockTag(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	userID, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	tagID, err := uuid.Parse(c.Param("tagId"))
	if err != nil {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}
	tag, err := d.Service.LockTag(c.Request.Context(), userID, tagID)
	if err != nil {
		internalError(c, err, d.log())
		return
	}
	if tag == nil {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}
	c.JSON(http.StatusOK, nfc.NewTagResponse(tag))
}

// unregisterTag handles DELETE /api/nfc/tags/:tagId.
func (d Deps) unregisterTag(c *gin.Context) {
	user := currentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	userID, err := uuid.Parse(user.Id)
	if err != nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	tagID, err := uuid.Parse(c.Param("tagId"))
	if err != nil {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}
	deleted, err := d.Service.UnregisterTag(c.Request.Context(), userID, tagID)
	if err != nil {
		internalError(c, err, d.log())
		return
	}
	if !deleted {
		c.JSON(http.StatusNotFound, nfcNotFound("NFC tag not found."))
		return
	}
	c.Status(http.StatusNoContent)
}

type createEncryptedTagRequest struct {
	UID            string     `json:"uid"`
	SunKey         string     `json:"sunKey"`
	AssignedUserID *uuid.UUID `json:"assignedUserId"`
}

// createEncryptedTag handles POST /api/admin/nfc/tags.
func (d Deps) createEncryptedTag(c *gin.Context) {
	var request createEncryptedTagRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, validationError("uid", "The request body is invalid."))
		return
	}
	if strings.TrimSpace(request.UID) == "" || len(request.UID) > 64 {
		c.JSON(http.StatusBadRequest, validationError("uid", "The Uid field is required."))
		return
	}

	sunKey, err := nfc.ParseSunKey(request.SunKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, validationError("sun_key", err.Error()))
		return
	}

	tag, err := d.Service.RegisterEncryptedTag(c.Request.Context(), request.UID, sunKey, request.AssignedUserID)
	if err != nil {
		switch {
		case errors.Is(err, nfc.ErrTagExists):
			c.JSON(http.StatusConflict, errs.New("PASSPORT_NFC_TAG_EXISTS", err.Error(), http.StatusConflict))
		case errors.Is(err, nfc.ErrInvalidSunKey), errors.Is(err, nfc.ErrEmptySunKey):
			c.JSON(http.StatusBadRequest, validationError("sun_key", err.Error()))
		default:
			internalError(c, err, d.log())
		}
		return
	}
	c.JSON(http.StatusOK, nfc.NewEncryptedTagDTO(tag))
}

// listEncryptedTags handles GET /api/admin/nfc/tags.
func (d Deps) listEncryptedTags(c *gin.Context) {
	tags, err := d.Service.ListAllEncryptedTags(c.Request.Context())
	if err != nil {
		internalError(c, err, d.log())
		return
	}
	response := make([]nfc.EncryptedTagDTO, 0, len(tags))
	for i := range tags {
		response = append(response, nfc.NewEncryptedTagDTO(&tags[i]))
	}
	c.JSON(http.StatusOK, response)
}
