package statusctl

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/models"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/permission"
	"src.solsynth.dev/sosys/stargate/ports"
	"src.solsynth.dev/sosys/stargate/store"
)

// AccountLookup is the account-resolution surface shared by the public status
// read and the activity routes' :identifier probe.
type AccountLookup interface {
	GetAccountWithProfile(ctx context.Context, id uuid.UUID) (*model.Account, error)
	GetAccountWithProfileByNameFold(ctx context.Context, name string) (*model.Account, error)
	GetAccountNameHistoryOwner(ctx context.Context, name string) (*model.Account, error)
}

var _ AccountLookup = (*store.Store)(nil)

// statusRequest mirrors AccountController.StatusRequest. The MaxLength
// attributes are enforced as ASP.NET model-state validation below.
type statusRequest struct {
	Attitude      model.StatusAttitude `json:"attitude"`
	Type          model.StatusType     `json:"type"`
	IsAutomated   bool                 `json:"is_automated"`
	Label         *string              `json:"label"`
	Symbol        *string              `json:"symbol"`
	AppIdentifier *string              `json:"app_identifier"`
	IconId        *string              `json:"icon_id"`
	BackgroundId  *string              `json:"background_id"`
	Meta          map[string]any       `json:"meta"`
	ClearedAt     *model.Time          `json:"cleared_at"`
}

func (d Deps) loader() Loader {
	return Loader{Store: d.Store, Presence: d.Presence}
}

// registerStatuses mounts /api/accounts/me/statuses and the public
// /api/accounts/:name/statuses read (AccountEventController's status actions
// plus AccountController.GetOtherStatus).
func registerStatuses(api *gin.RouterGroup, d Deps) {
	me := api.Group("/accounts/me")
	me.GET("/statuses", middleware.RequireAuth(), d.getMyStatus)
	me.PATCH("/statuses", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.AccountsStatusesUpdate), d.updateStatus)
	me.POST("/statuses", middleware.RequireAuth(), middleware.AskPermission(d.Perm, permission.AccountsStatusesCreate), d.createStatus)
	me.DELETE("/statuses", middleware.RequireAuth(), d.deleteStatus)

	api.GET("/accounts/:name/statuses", d.getOtherStatus)
}

// getMyStatus serves GET /api/accounts/me/statuses: the owner is the viewer,
// so the device list is always visible.
func (d Deps) getMyStatus(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	status, err := d.loader().Status(c.Request.Context(), accountID, &accountID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

// updateStatus serves PATCH /api/accounts/me/statuses.
//
// Validation order matches the C# action exactly: the automated-with-app
// rejection runs first, then the "no active status" 404, then the
// "automated status cannot be edited without its app identifier" rejection,
// and only then are the file references resolved.
func (d Deps) updateStatus(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	var req statusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"request": {"The request body is invalid."}}))
		return
	}
	if !validateStatusRequest(c, &req) {
		return
	}
	ctx := c.Request.Context()

	if req.IsAutomated && req.AppIdentifier != nil {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_STATUS_AUTOMATED_NO_UPDATE", "Automated status cannot be updated.", http.StatusBadRequest))
		return
	}

	status, err := d.Store.GetCurrentStatus(ctx, accountID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		internalError(c, err)
		return
	}
	if status == nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_STATUS_NOT_FOUND", "Status not found.", http.StatusNotFound))
		return
	}
	if status.IsAutomated && req.AppIdentifier == nil {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_STATUS_AUTOMATED_NO_UPDATE", "Automated status cannot be updated.", http.StatusBadRequest))
		return
	}

	// Snapshot the row before the mutations so the broadcaster can tell
	// whether the write actually changed anything (StatusChanged re-reads the
	// current row and skips identical snapshots).
	previous := cloneStatus(status)
	status.Attitude = req.Attitude
	status.Type = req.Type
	status.IsAutomated = req.IsAutomated
	status.Label = req.Label
	status.Symbol = req.Symbol
	status.AppIdentifier = req.AppIdentifier
	status.Meta = req.Meta
	status.ClearedAt = req.ClearedAt

	if req.IconId != nil {
		icon, ok := d.resolveStatusFile(c, *req.IconId, "PASSPORT_ICON_NOT_FOUND", "Icon not found.")
		if !ok {
			return
		}
		status.Icon = icon
	}
	if req.BackgroundId != nil {
		background, ok := d.resolveStatusFile(c, *req.BackgroundId, "PASSPORT_BACKGROUND_NOT_FOUND", "Background not found.")
		if !ok {
			return
		}
		status.Background = background
	}

	if err := d.Store.UpdateStatus(ctx, status); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_STATUS_NOT_FOUND", "Status not found.", http.StatusNotFound))
			return
		}
		internalError(c, err)
		return
	}
	d.broadcastStatus(ctx, accountID, previous)
	c.JSON(http.StatusOK, status)
}

// createStatus serves POST /api/accounts/me/statuses.
//
// An automated request carrying an app identifier never stacks rows: the same
// app's active automated status is updated in place, a different app's
// automated status is cleared (and a new row created), and a manually set
// status is returned untouched.
func (d Deps) createStatus(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	var req statusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{"request": {"The request body is invalid."}}))
		return
	}
	if !validateStatusRequest(c, &req) {
		return
	}
	ctx := c.Request.Context()
	// previous is the row the write replaces; nil when the account had none.
	// It is snapshotted before any mutation so the broadcaster can skip
	// no-op writes.
	var previous *model.AccountStatus

	if req.IsAutomated && req.AppIdentifier != nil {
		existing, err := d.Store.GetCurrentStatus(ctx, accountID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			internalError(c, err)
			return
		}
		if existing != nil && existing.IsAutomated {
			if existing.AppIdentifier != nil && *existing.AppIdentifier == *req.AppIdentifier {
				previous = cloneStatus(existing)
				existing.Attitude = req.Attitude
				existing.Type = req.Type
				existing.IsAutomated = req.IsAutomated
				existing.Meta = req.Meta
				existing.Label = req.Label
				existing.Symbol = req.Symbol
				existing.AppIdentifier = req.AppIdentifier
				existing.ClearedAt = req.ClearedAt

				// The C# update-in-place branch ignores a missing file
				// instead of rejecting the request.
				if req.IconId != nil {
					if icon, ok := d.optionalFile(ctx, *req.IconId); ok {
						existing.Icon = icon
					}
				}
				if req.BackgroundId != nil {
					if background, ok := d.optionalFile(ctx, *req.BackgroundId); ok {
						existing.Background = background
					}
				}

				if err := d.Store.UpdateStatus(ctx, existing); err != nil {
					if errors.Is(err, store.ErrNotFound) {
						c.JSON(http.StatusNotFound, errs.New("PASSPORT_STATUS_NOT_FOUND", "Status not found.", http.StatusNotFound))
						return
					}
					internalError(c, err)
					return
				}
				d.broadcastStatus(ctx, accountID, previous)
				c.JSON(http.StatusOK, existing)
				return
			}
			// Another app's automated status: clear it and continue creating.
			previous = cloneStatus(existing)
			if err := d.Store.ClearStatus(ctx, accountID, uuid.MustParse(existing.Id)); err != nil && !errors.Is(err, store.ErrNotFound) {
				internalError(c, err)
				return
			}
		} else if existing != nil {
			// Do not override manually set status with automated ones.
			c.JSON(http.StatusOK, existing)
			return
		}
	}
	// CreateStatus clears the account's active row, so a manual POST also
	// replaces whatever status the account had.
	if previous == nil {
		current, err := d.Store.GetCurrentStatus(ctx, accountID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			internalError(c, err)
			return
		}
		previous = current
	}

	status := &model.AccountStatus{
		AccountId:     accountID.String(),
		Attitude:      req.Attitude,
		Type:          req.Type,
		IsAutomated:   req.IsAutomated,
		Label:         req.Label,
		Symbol:        req.Symbol,
		Meta:          req.Meta,
		AppIdentifier: req.AppIdentifier,
		ClearedAt:     req.ClearedAt,
		IsCustomized:  true,
		IsInvisible:   req.Type == model.StatusTypeInvisible,
		IsNotDisturb:  req.Type == model.StatusTypeDoNotDisturb,
	}
	if req.IconId != nil {
		if icon, ok := d.optionalFile(ctx, *req.IconId); ok {
			status.Icon = icon
		}
	}
	if req.BackgroundId != nil {
		if background, ok := d.optionalFile(ctx, *req.BackgroundId); ok {
			status.Background = background
		}
	}

	if err := d.Store.CreateStatus(ctx, status); err != nil {
		internalError(c, err)
		return
	}
	d.broadcastStatus(ctx, accountID, previous)
	c.JSON(http.StatusOK, status)
}

// deleteStatus serves DELETE /api/accounts/me/statuses?app=: without an app it
// clears the newest active status, with one it clears that app's automated
// status.
func (d Deps) deleteStatus(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := currentAccountID(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	ctx := c.Request.Context()
	app, _ := queryAny(c, "app")

	var status *model.AccountStatus
	var err error
	if app != "" {
		status, err = d.Store.GetCurrentStatusByApp(ctx, accountID, app)
	} else {
		status, err = d.Store.GetCurrentStatus(ctx, accountID)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		internalError(c, err)
		return
	}
	if status == nil {
		c.JSON(http.StatusNotFound, errs.New("PASSPORT_STATUS_NOT_FOUND", "Status not found.", http.StatusNotFound))
		return
	}
	statusID, parseErr := uuid.Parse(status.Id)
	if parseErr != nil {
		internalError(c, parseErr)
		return
	}
	if err := d.Store.ClearStatus(ctx, accountID, statusID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, errs.New("PASSPORT_STATUS_NOT_FOUND", "Status not found.", http.StatusNotFound))
			return
		}
		internalError(c, err)
		return
	}
	d.broadcastStatus(ctx, accountID, status)
	c.Status(http.StatusNoContent)
}

// getOtherStatus serves GET /api/accounts/:name/statuses. It is public; an
// unknown name is a 400 PASSPORT_ACCOUNT_NOT_FOUND (the C# action, unlike the
// 404 its activity lookup returns), and an invisible status degrades to
// Default for everyone but the owner.
func (d Deps) getOtherStatus(c *gin.Context) {
	name := c.Param("name")
	ctx := c.Request.Context()
	account, err := lookupAccount(ctx, d.Accounts, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			detail := name
			c.JSON(http.StatusBadRequest, &errs.ApiError{
				Code: "PASSPORT_ACCOUNT_NOT_FOUND", Message: "Account not found.",
				Status: http.StatusBadRequest, Detail: &detail,
			})
			return
		}
		internalError(c, err)
		return
	}
	ownerID, err := uuid.Parse(account.Id)
	if err != nil {
		internalError(c, err)
		return
	}
	viewer := currentViewer(c)
	status, err := d.loader().Status(ctx, ownerID, viewer)
	if err != nil {
		internalError(c, err)
		return
	}
	// The C# rewrites the type, so an invisible account is not broadcast as
	// invisible through this public read.
	if status.Type == model.StatusTypeInvisible {
		status.Type = model.StatusTypeDefault
		status.IsInvisible = false
	}
	c.JSON(http.StatusOK, status)
}

// lookupAccount resolves a name (or id) probe the way the Passport actions do:
// GUID probes load by id, names fall back to the most recent former owner.
func lookupAccount(ctx context.Context, accounts AccountLookup, probe string) (*model.Account, error) {
	if accounts == nil {
		return nil, store.ErrNotFound
	}
	if id, err := uuid.Parse(probe); err == nil {
		return accounts.GetAccountWithProfile(ctx, id)
	}
	account, err := accounts.GetAccountWithProfileByNameFold(ctx, probe)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return accounts.GetAccountNameHistoryOwner(ctx, probe)
		}
		return nil, err
	}
	return account, nil
}

// validateStatusRequest mirrors the DataAnnotations on StatusRequest.
func validateStatusRequest(c *gin.Context, req *statusRequest) bool {
	fields := map[string][]string{}
	if req.Label != nil && len([]rune(*req.Label)) > 1024 {
		fields["label"] = []string{"The field Label must be a string with a maximum length of 1024."}
	}
	if req.Symbol != nil && len([]rune(*req.Symbol)) > 128 {
		fields["symbol"] = []string{"The field Symbol must be a string with a maximum length of 128."}
	}
	if req.AppIdentifier != nil && len([]rune(*req.AppIdentifier)) > 4096 {
		fields["app_identifier"] = []string{"The field AppIdentifier must be a string with a maximum length of 4096."}
	}
	if req.IconId != nil && len([]rune(*req.IconId)) > 32 {
		fields["icon_id"] = []string{"The field IconId must be a string with a maximum length of 32."}
	}
	if req.BackgroundId != nil && len([]rune(*req.BackgroundId)) > 32 {
		fields["background_id"] = []string{"The field BackgroundId must be a string with a maximum length of 32."}
	}
	if len(fields) == 0 {
		return true
	}
	c.JSON(http.StatusBadRequest, errs.Validation(fields))
	return false
}

// resolveStatusFile fetches an icon/background file reference, answering the
// C# code when the file is missing (an unset Drive degrades to "not found").
func (d Deps) resolveStatusFile(c *gin.Context, fileID, code, message string) (*model.SnCloudFileReferenceObject, bool) {
	file, err := d.fetchFile(c.Request.Context(), fileID)
	if err != nil {
		internalError(c, err)
		return nil, false
	}
	if file == nil {
		c.JSON(http.StatusBadRequest, errs.New(code, message, http.StatusBadRequest))
		return nil, false
	}
	return file, true
}

// optionalFile resolves a file reference, reporting false when it is missing
// or Drive is not configured (the C# create/update-in-place branches skip such
// references instead of failing).
func (d Deps) optionalFile(ctx context.Context, fileID string) (*model.SnCloudFileReferenceObject, bool) {
	file, err := d.fetchFile(ctx, fileID)
	if err != nil || file == nil {
		return nil, false
	}
	return file, true
}

// fetchFile mirrors profilectl's resolveFile: the Drive port when a Files
// provider is registered, otherwise the direct gRPC client.
func (d Deps) fetchFile(ctx context.Context, fileID string) (*model.SnCloudFileReferenceObject, error) {
	if d.Files != nil && d.Files.Available() {
		file, err := d.Files.Resolve(ctx, fileID)
		if errors.Is(err, ports.ErrUnavailable) {
			return nil, nil
		}
		return file, err
	}
	if d.Clients != nil && d.Clients.Drive != nil {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		file, err := d.Clients.Drive.GetFile(ctx, &gen.DyGetFileRequest{Id: fileID})
		if err != nil || file == nil {
			return nil, err
		}
		ref := models.FromProtoValue(file)
		return &ref, nil
	}
	// No Drive at all: the C# would see a null file, i.e. "not found".
	return nil, nil
}

// broadcastStatus publishes a status write. previous is the pre-write snapshot
// (nil when the account had no active status): the broadcaster re-reads the
// current row, compares, and drops the cache keys, so passing the post-write
// object here would suppress every notification.
func (d Deps) broadcastStatus(ctx context.Context, accountID uuid.UUID, previous *model.AccountStatus) {
	if d.Broadcast == nil {
		return
	}
	if err := d.Broadcast.StatusChanged(ctx, accountID, "", previous, time.Now().UTC()); err != nil {
		if d.Log != nil {
			d.Log.Warn("broadcast account status", "account", accountID, "error", err)
		}
	}
}

// cloneStatus copies the persisted half of a status so a mutation does not
// rewrite the snapshot handed to the broadcaster.
func cloneStatus(status *model.AccountStatus) *model.AccountStatus {
	if status == nil {
		return nil
	}
	clone := *status
	if status.OnlineDevices != nil {
		clone.OnlineDevices = append([]model.OnlineDevice(nil), status.OnlineDevices...)
	}
	return &clone
}
