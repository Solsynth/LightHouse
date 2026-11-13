package librarysync

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/store"
	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// libraryAccountIDKey is where the dev account header stores its account id.
const libraryAccountIDKey = "library_account_id"

// registerRoutes mounts /api/library. With a configured dev account header the
// fleet guard chain is replaced entirely: that switch exists for local runs
// without a Stargate and is documented as deployment-unsafe.
func (i *instance) registerRoutes(host *module.Host[core.Services]) {
	group := host.API.Group("/library")

	guards := []gin.HandlerFunc{
		authn.RequireAuth(),
		host.Core.Auth.RequirePermission("library.sync"),
	}
	if header := strings.TrimSpace(i.core.Config.LibrarySync.DevAccountHeader); header != "" {
		i.log.Warn("pulsar library sync dev account header enabled; fleet auth bypassed", "header", header)
		guards = []gin.HandlerFunc{devAccountHeader(header)}
	}

	h := &handler{core: i.core, store: i.core.Store, log: i.log}
	group.Handle(http.MethodPost, "/push", append(guards, h.push)...)
	group.Handle(http.MethodGet, "/pull", append(guards, h.pull)...)
	group.Handle(http.MethodGet, "/status", append(guards, h.status)...)
}

// devAccountHeader trusts a UUID request header as the account id.
func devAccountHeader(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		value := strings.TrimSpace(c.GetHeader(name))
		parsed, err := uuid.Parse(value)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errs.Unauthorized(""))
			return
		}
		c.Set(libraryAccountIDKey, parsed)
		c.Next()
	}
}

// accountIDOf returns the account id of the request, preferring the dev header
// and falling back to the authenticated session.
func accountIDOf(c *gin.Context) (uuid.UUID, bool) {
	if value, ok := c.Get(libraryAccountIDKey); ok {
		if id, ok := value.(uuid.UUID); ok {
			return id, true
		}
	}
	id, err := uuid.Parse(authn.AccountID(c))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// handler owns the push, pull and status endpoints.
type handler struct {
	core  *core.Services
	store *store.Store
	log   *slog.Logger
}

// push applies a batch of local changes under last-write-wins.
func (h *handler) push(c *gin.Context) {
	accountID, ok := accountIDOf(c)
	if !ok {
		httpx.Fail(c, errs.Unauthorized(""))
		return
	}

	var request PushRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	deviceID := strings.TrimSpace(request.DeviceID)
	if deviceID == "" {
		httpx.Fail(c, errs.Validation(map[string][]string{"device_id": {"device_id is required."}}))
		return
	}
	maxBatch := h.core.Config.LibrarySync.MaxBatch
	if maxBatch <= 0 {
		maxBatch = 500
	}
	if len(request.Changes) > maxBatch {
		httpx.Fail(c, errs.Validation(map[string][]string{
			"changes": {"batch exceeds maxBatch of " + strconv.Itoa(maxBatch)},
		}))
		return
	}
	if err := validateBatch(request.Changes); err != nil {
		var failure changeError
		message := err.Error()
		if errors.As(err, &failure) {
			message = failure.Error()
		}
		httpx.Fail(c, errs.Validation(map[string][]string{"changes": {message}}))
		return
	}

	rejected := []int{}
	finalRev := int64(0)
	now := time.Now().UTC()

	err := h.store.DB.Transaction(func(tx *gorm.DB) error {
		base, err := store.NextLibrarySyncRevs(tx, accountID, len(request.Changes))
		if err != nil {
			return err
		}
		applied := &applier{tx: tx, accountID: accountID, deviceID: deviceID, now: now}
		for index, change := range request.Changes {
			ok, err := applied.applyOne(change, base+int64(index))
			if err != nil {
				return err
			}
			if !ok {
				rejected = append(rejected, index)
			}
		}
		finalRev, err = store.CurrentLibrarySyncRev(tx, accountID)
		return err
	})
	if err != nil {
		h.log.Error("rhythm sync push failed", "error", err)
		httpx.Internal(c)
		return
	}

	httpx.OK(c, PushResponse{Rev: finalRev, Rejected: rejected})
}
