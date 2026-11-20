package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"src.solsynth.dev/sosys/filesystem/database"
	"src.solsynth.dev/sosys/filesystem/service"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/auth"
)

func principalFromContext(c *gin.Context) *service.PrincipalSet {
	result, _, ok := auth.GetAuth(c)
	if !ok || result == nil {
		p := service.NewPrincipalSet(nil, nil)
		if shareLink := strings.TrimSpace(c.Query("share_token")); shareLink != "" {
			p.ShareLinkID = shareLink
		}
		return p
	}
	p := service.NewPrincipalSet(result.Account, result.Session)
	if shareLink := strings.TrimSpace(c.Query("share_token")); shareLink != "" {
		p.ShareLinkID = shareLink
	}
	return p
}

// hasFilePermission reports whether the request's caller holds every bit in
// required on file. Unlike CanAccessFile's verb strings it asks for exactly the
// capability the handler needs.
func hasFilePermission(c *gin.Context, files *service.FileService, file *database.CloudFile, required service.PermissionMask) bool {
	if file == nil {
		return false
	}
	return files.CanAccessFileMask(c.Request.Context(), principalFromContext(c), file, required)
}

// requireParentWrite authorizes creating a child under parentID. A nil parent
// means the namespace root, which quota/membership checks already gate.
func requireParentWrite(c *gin.Context, files *service.FileService, parentID *string, workspaceID *string) bool {
	if parentID == nil || strings.TrimSpace(*parentID) == "" {
		return true
	}
	parent, err := files.GetFileInWorkspace(strings.TrimSpace(*parentID), workspaceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return false
	}
	if !hasFilePermission(c, files, parent, service.PermWrite) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return false
	}
	return true
}

// @Summary Get file ACL
// @Tags files
// @Produce json
// @Param id path string true "File ID"
// @Success 200 {object} service.FileACLDetail
// @Router /api/files/{id}/acl [get]
func getFileACL(c *gin.Context, files *service.FileService) {
	result, _, ok := auth.GetAuth(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	file, err := getRequestedFile(c, files, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	caller := service.NewPrincipalSet(result.Account, result.Session)
	if !files.CanAccessFileMask(c.Request.Context(), caller, file, service.PermManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	detail, err := files.GetFileACL(c.Request.Context(), caller, file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, detail)
}

type replaceFileACLRequest struct {
	InheritACL *bool                   `json:"inherit_acl"`
	Entries    []service.ACLEntryInput `json:"entries"`
}

// @Summary Replace file ACL
// @Tags files
// @Accept json
// @Produce json
// @Param id path string true "File ID"
// @Success 200 {object} service.FileACLDetail
// @Router /api/files/{id}/acl [put]
func replaceFileACL(c *gin.Context, files *service.FileService) {
	result, _, ok := auth.GetAuth(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	file, err := getRequestedFile(c, files, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	caller := service.NewPrincipalSet(result.Account, result.Session)
	if !files.CanAccessFileMask(c.Request.Context(), caller, file, service.PermManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	var req replaceFileACLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var actorID *uuid.UUID
	if parsed, err := uuid.Parse(result.Account.GetId()); err == nil {
		actorID = &parsed
	}
	if err := files.ReplaceFileACL(c.Request.Context(), file, req.InheritACL, req.Entries, actorID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := getRequestedFile(c, files, file.ID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	detail, err := files.GetFileACL(c.Request.Context(), caller, updated)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, detail)
}

// @Summary Upsert one file ACL entry
// @Tags files
// @Accept json
// @Produce json
// @Param id path string true "File ID"
// @Success 200 {object} database.FileACLEntry
// @Router /api/files/{id}/acl/entries [post]
func upsertFileACLEntry(c *gin.Context, files *service.FileService) {
	result, _, ok := auth.GetAuth(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	file, err := getRequestedFile(c, files, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	caller := service.NewPrincipalSet(result.Account, result.Session)
	eval, err := files.EvaluateFile(c.Request.Context(), caller, file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if eval.Mask&(service.PermManage|service.PermShare) == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	var req service.ACLEntryInput
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Callers with PermShare (but not PermManage) may only grant up to their
	// own permissions and cannot add deny entries.
	if eval.Mask&service.PermManage == 0 {
		if strings.EqualFold(strings.TrimSpace(req.Effect), service.ACLEffectDeny) {
			c.JSON(http.StatusForbidden, gin.H{"error": "manage permission is required to add deny rules"})
			return
		}
		requestedMask := service.PermissionMask(req.Permissions)
		if requestedMask == 0 && strings.TrimSpace(req.Role) != "" {
			if parsed, err := service.ParsePermissionMask(req.Role); err == nil {
				requestedMask = parsed
			}
		}
		if requestedMask&^eval.Mask != 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "cannot grant permissions exceeding your own"})
			return
		}
	}
	var actorID *uuid.UUID
	if parsed, err := uuid.Parse(result.Account.GetId()); err == nil {
		actorID = &parsed
	}
	entry, err := files.UpsertFileACLEntry(c.Request.Context(), file, req, actorID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, entry)
}

// @Summary Delete one file ACL entry
// @Tags files
// @Produce json
// @Param id path string true "File ID"
// @Param entryId path string true "ACL Entry ID"
// @Success 200 {object} map[string]bool
// @Router /api/files/{id}/acl/entries/{entryId} [delete]
func deleteFileACLEntry(c *gin.Context, files *service.FileService) {
	result, _, ok := auth.GetAuth(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	file, err := getRequestedFile(c, files, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	caller := service.NewPrincipalSet(result.Account, result.Session)
	if !files.CanAccessFileMask(c.Request.Context(), caller, file, service.PermManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	if err := files.DeleteFileACLEntry(c.Request.Context(), file.ID, c.Param("entryId")); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "acl entry not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// @Summary List files shared with the current account
// @Tags files
// @Produce json
// @Success 200 {array} database.CloudFile
// @Router /api/files/shared-with-me [get]
func listSharedWithMe(c *gin.Context, files *service.FileService) {
	result, _, ok := auth.GetAuth(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	caller := service.NewPrincipalSet(result.Account, result.Session)
	filters := parseListQuery(c, 0, 50)
	items, total, err := files.ListSharedWithMe(c.Request.Context(), caller, fileListOptions(filters))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("X-Total", strconv.FormatInt(total, 10))
	c.JSON(http.StatusOK, items)
}
