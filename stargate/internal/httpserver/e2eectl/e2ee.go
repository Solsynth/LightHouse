package e2eectl

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/stargate/internal/auth"
	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/grpcclient"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
	"src.solsynth.dev/sosys/stargate/internal/store"
)

// Deps carries the dependencies the E2EE controller uses.
type Deps struct {
	Store   *store.Store
	Events  auth.EventBus
	Clients *grpcclient.Clients
	Log     *slog.Logger
}

// Register mounts the /api/e2ee/mls/* routes (E2eeController.cs). Every route
// requires an authenticated token; device-scoped delivery and group-state
// routes additionally require the X-Device-Id header.
func Register(api *gin.RouterGroup, d Deps) {
	s := NewService(d.Store, d.Events, d.Clients, d.Log)
	g := api.Group("/e2ee")
	g.Use(middleware.RequireAuth())
	{
		g.PUT("/mls/devices/me/kps", s.handlePublishMlsKeyPackage)
		g.GET("/mls/kp/status", s.handleGetMlsKeyPackageStatus)
		g.GET("/mls/keys/:accountId/devices", s.handleListMlsKeyPackagesByDevice)
		g.POST("/mls/users/ready/batch", s.handleBatchCheckMlsUsersReady)
		g.GET("/mls/users/:accountId/ready", s.handleCheckMlsUserReady)
		g.GET("/mls/groups/:groupId/devices/capable", s.handleGetCapableDevices)
		g.POST("/mls/groups/:groupId/bootstrap", s.handleBootstrapMlsGroup)
		g.POST("/mls/groups/:groupId/commit", s.handleCommitMlsGroup)
		g.POST("/mls/groups/:groupId/welcome/fanout", s.handleFanoutMlsWelcome)
		g.POST("/mls/groups/:groupId/reshare-required", s.handleMarkMlsReshareRequired)
		g.GET("/mls/devices/me/reshare-required", s.handleGetMyDeviceReshareStatus)
		g.POST("/mls/devices/me/reshare-required/:groupId/complete", s.handleCompleteMyDeviceReshare)
		g.PUT("/mls/groups/:groupId/groupinfo", s.handleUploadGroupInfo)
		g.GET("/mls/groups/:groupId/groupinfo", s.handleGetGroupInfo)
		g.POST("/mls/messages/fanout", s.handleSendMlsFanout)
		g.POST("/mls/groups/:groupId/commit/fanout", s.handleFanoutMlsCommit)
		g.POST("/mls/groups/:groupId/messages/fanout", s.handleFanoutMlsMessageToGroup)
		g.GET("/mls/envelopes/pending", s.handleGetMlsPendingByDevice)
		g.POST("/mls/envelopes/:envelopeId/ack", s.handleAckMlsEnvelope)
		g.POST("/mls/devices/:deviceId/revoke", s.handleRevokeMlsDevice)
		g.POST("/mls/devices/:deviceId/membership", s.handleAddMlsDeviceMembership)
		g.POST("/mls/groups/:groupId/reset", s.handleResetMlsGroup)
	}
}

// --- shared helpers ---

// unauthorized mirrors the controller's AUTH_UNAUTHORIZED checks.
func unauthorized(c *gin.Context) {
	c.JSON(http.StatusUnauthorized, errs.New("AUTH_UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
}

// requireUser mirrors the CurrentUser/CurrentSession item checks in the
// controller. After middleware.RequireAuth the user is always present; the
// session check is only meaningful where the C# requires it.
func requireUser(c *gin.Context, requireSession bool) bool {
	if middleware.CurrentUser(c.Request.Context()) == nil {
		unauthorized(c)
		return false
	}
	if requireSession && middleware.CurrentSession(c.Request.Context()) == nil {
		unauthorized(c)
		return false
	}
	return true
}

// requireDeviceID mirrors the E2EE_DEVICE_ID_REQUIRED header checks.
func requireDeviceID(c *gin.Context) (string, bool) {
	deviceID := strings.TrimSpace(c.GetHeader("X-Device-Id"))
	if deviceID == "" {
		c.JSON(http.StatusBadRequest, errs.New("E2EE_DEVICE_ID_REQUIRED", "X-Device-Id header is required.", http.StatusBadRequest))
		return "", false
	}
	return deviceID, true
}

// bindJSON mirrors ASP.NET model binding: a malformed body yields a 400
// validation error before any action logic runs.
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, errs.New("VALIDATION_ERROR", "The request data is invalid.", http.StatusBadRequest))
		return false
	}
	return true
}

// validationError writes a 400 with per-field messages (the ASP.NET
// VALIDATION_ERROR shape).
func validationError(c *gin.Context, fieldErrors map[string][]string) {
	c.JSON(http.StatusBadRequest, errs.Validation(fieldErrors))
}

// serviceError maps service failures: the C# throws unhandled exceptions for
// these (ASP.NET yields an empty 500), so Stargate returns a generic 500
// ApiError and logs the real message.
func (s *Service) serviceError(c *gin.Context, err error) {
	var se *ServiceError
	if errors.As(err, &se) {
		s.logf().Warn("e2ee business rule failure", "error", se.Message)
	} else {
		s.logf().Error("e2ee unexpected error", "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "An internal error occurred.", http.StatusInternalServerError))
}

// requireGroupMember mirrors the controller's IsMlsGroupMemberAsync + Forbid()
// guard (403 with an empty body, like ASP.NET's Forbid result).
func (s *Service) requireGroupMember(c *gin.Context, accountID, deviceID, groupID string) bool {
	ok, err := s.IsMlsGroupMember(c.Request.Context(), accountID, deviceID, groupID)
	if err != nil {
		s.serviceError(c, err)
		return false
	}
	if !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return false
	}
	return true
}

// parseRouteUUID mirrors the ASP.NET {id:guid} route constraint: a non-GUID
// fails to match the route and produces a 404 with an empty body.
func parseRouteUUID(c *gin.Context, raw string) (string, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		c.Status(http.StatusNotFound)
		return "", false
	}
	return id.String(), true
}

// --- handlers (route-for-route from E2eeController.cs) ---

// PUT /api/e2ee/mls/devices/me/kps — PublishMlsKeyPackage.
func (s *Service) handlePublishMlsKeyPackage(c *gin.Context) {
	if !requireUser(c, true) {
		return
	}
	var body publishMlsKeyPackageBody
	if !bindJSON(c, &body) {
		return
	}
	if errors := validatePublishKeyPackage(&body); errors != nil {
		validationError(c, errors)
		return
	}
	if body.Ciphersuite == "" {
		body.Ciphersuite = DefaultMlsCiphersuite
	}
	user := middleware.CurrentUser(c.Request.Context())
	result, err := s.PublishMlsKeyPackage(c.Request.Context(), user.Id, body.DeviceId, body.DeviceLabel, body.KeyPackage, body.Ciphersuite, body.Meta)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// GET /api/e2ee/mls/kp/status — GetMlsKeyPackageStatus.
func (s *Service) handleGetMlsKeyPackageStatus(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	result, err := s.MlsKeyPackageStatus(c.Request.Context(), user.Id)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// GET /api/e2ee/mls/keys/:accountId/devices — ListMlsKeyPackagesByDevice
// (consume defaults to true).
func (s *Service) handleListMlsKeyPackagesByDevice(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	accountID, ok := parseRouteUUID(c, c.Param("accountId"))
	if !ok {
		return
	}
	consume := true
	if raw := c.Query("consume"); raw != "" {
		if parsed, err := strconv.ParseBool(raw); err == nil {
			consume = parsed
		}
	}
	user := middleware.CurrentUser(c.Request.Context())
	requesterID := user.Id
	result, err := s.ListMlsDeviceKeyPackages(c.Request.Context(), accountID, &requesterID, consume)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/e2ee/mls/users/ready/batch — BatchCheckMlsUsersReady.
func (s *Service) handleBatchCheckMlsUsersReady(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body batchCheckMlsReadyRequest
	if !bindJSON(c, &body) {
		return
	}
	if errors := validateBatchCheck(&body); errors != nil {
		validationError(c, errors)
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	requesterID := user.Id
	users := make([]MlsUserAvailability, 0, len(body.AccountIds))
	for _, accountID := range body.AccountIds {
		packages, err := s.ListMlsDeviceKeyPackages(c.Request.Context(), accountID, &requesterID, false)
		if err != nil {
			s.serviceError(c, err)
			return
		}
		users = append(users, MlsUserAvailability{
			AccountId:            accountID,
			IsReady:              len(packages) > 0,
			AvailableKeyPackages: len(packages),
		})
	}
	c.JSON(http.StatusOK, BatchCheckMlsReady{Users: users})
}

// GET /api/e2ee/mls/users/:accountId/ready — CheckMlsUserReady.
func (s *Service) handleCheckMlsUserReady(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	accountID, ok := parseRouteUUID(c, c.Param("accountId"))
	if !ok {
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	requesterID := user.Id
	packages, err := s.ListMlsDeviceKeyPackages(c.Request.Context(), accountID, &requesterID, false)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, CheckMlsReady{
		IsReady:              len(packages) > 0,
		AvailableKeyPackages: len(packages),
	})
}

// GET /api/e2ee/mls/groups/:groupId/devices/capable — GetCapableDevices.
func (s *Service) handleGetCapableDevices(c *gin.Context) {
	result, err := s.GetCapableDevices(c.Request.Context(), c.Param("groupId"))
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/e2ee/mls/groups/:groupId/bootstrap — BootstrapMlsGroup.
func (s *Service) handleBootstrapMlsGroup(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body bootstrapMlsGroupBody
	if !bindJSON(c, &body) {
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	stateVersion := int64(1)
	if body.StateVersion != nil {
		stateVersion = *body.StateVersion
	}
	result, err := s.BootstrapMlsGroup(c.Request.Context(), user.Id, c.Param("groupId"), body.Epoch, stateVersion, body.Meta)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/e2ee/mls/groups/:groupId/commit — CommitMlsGroup.
func (s *Service) handleCommitMlsGroup(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body commitMlsGroupBody
	if !bindJSON(c, &body) {
		return
	}
	if body.Reason == "" {
		body.Reason = "client_commit"
	}
	user := middleware.CurrentUser(c.Request.Context())
	result, err := s.CommitMlsGroup(c.Request.Context(), user.Id, c.Param("groupId"), body.Epoch, body.Reason, body.Meta)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	if result == nil {
		c.JSON(http.StatusNotFound, errs.New("E2EE_GROUP_NOT_FOUND", "MLS group was not found.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/e2ee/mls/groups/:groupId/welcome/fanout — FanoutMlsWelcome.
func (s *Service) handleFanoutMlsWelcome(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body fanoutMlsWelcomeBody
	if !bindJSON(c, &body) {
		return
	}
	if errs_ := validateFanoutItems(body.Payloads); errs_ != nil {
		validationError(c, errs_)
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	groupID := c.Param("groupId")
	user := middleware.CurrentUser(c.Request.Context())
	if !s.requireGroupMember(c, user.Id, deviceID, groupID) {
		return
	}
	payloads := make([]DeviceCiphertextEnvelope, 0, len(body.Payloads))
	for _, p := range body.Payloads {
		payloads = append(payloads, DeviceCiphertextEnvelope{
			RecipientDeviceID: derefOrEmpty(p.RecipientDeviceId),
			ClientMessageID:   p.ClientMessageId,
			Ciphertext:        p.Ciphertext,
			Header:            p.Header,
			Signature:         p.Signature,
			Meta:              p.Meta,
		})
	}
	result, err := s.FanoutMlsWelcome(c.Request.Context(), user.Id, deviceID, groupID,
		body.RecipientAccountId, timeOf(body.ExpiresAt), payloads)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/e2ee/mls/groups/:groupId/reshare-required — MarkMlsReshareRequired.
func (s *Service) handleMarkMlsReshareRequired(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body markMlsReshareRequiredBody
	if !bindJSON(c, &body) {
		return
	}
	if body.TargetDeviceId == "" {
		validationError(c, map[string][]string{"targetDeviceId": {"The TargetDeviceId field is required."}})
		return
	}
	result, err := s.MarkMlsReshareRequired(c.Request.Context(), c.Param("groupId"), body.TargetAccountId, body.TargetDeviceId, body.Epoch, body.Reason)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// GET /api/e2ee/mls/devices/me/reshare-required — GetMyDeviceReshareStatus.
func (s *Service) handleGetMyDeviceReshareStatus(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	result, err := s.GetDeviceReshareStatus(c.Request.Context(), user.Id, deviceID)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/e2ee/mls/devices/me/reshare-required/:groupId/complete —
// CompleteMyDeviceReshare.
func (s *Service) handleCompleteMyDeviceReshare(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	completed, err := s.CompleteMlsReshare(c.Request.Context(), user.Id, deviceID, c.Param("groupId"))
	if err != nil {
		s.serviceError(c, err)
		return
	}
	if !completed {
		c.JSON(http.StatusNotFound, errs.New("E2EE_RESHARE_NOT_FOUND", "Device reshare status was not found.", http.StatusNotFound))
		return
	}
	c.Status(http.StatusNoContent)
}

// PUT /api/e2ee/mls/groups/:groupId/groupinfo — UploadGroupInfo.
func (s *Service) handleUploadGroupInfo(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body uploadGroupInfoBody
	if !bindJSON(c, &body) {
		return
	}
	if body.GroupInfo == nil || body.RatchetTree == nil {
		var errs_ map[string][]string
		if body.GroupInfo == nil {
			errs_ = addFieldError(errs_, "groupInfo", "The GroupInfo field is required.")
		}
		if body.RatchetTree == nil {
			errs_ = addFieldError(errs_, "ratchetTree", "The RatchetTree field is required.")
		}
		validationError(c, errs_)
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	groupID := c.Param("groupId")
	user := middleware.CurrentUser(c.Request.Context())
	if !s.requireGroupMember(c, user.Id, deviceID, groupID) {
		return
	}
	result, err := s.UploadGroupInfo(c.Request.Context(), groupID, body.GroupInfo, body.RatchetTree, body.Epoch)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	if !result.Success {
		e := errs.New("E2EE_MLS_EPOCH_MISMATCH", "Epoch mismatch when uploading group info.", http.StatusConflict)
		detail := fmt.Sprintf("Current epoch: %d, requested epoch: %d", result.Epoch, body.Epoch)
		e.Detail = &detail
		c.JSON(http.StatusConflict, e)
		return
	}
	c.JSON(http.StatusOK, result)
}

// GET /api/e2ee/mls/groups/:groupId/groupinfo — GetGroupInfo.
func (s *Service) handleGetGroupInfo(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	groupID := c.Param("groupId")
	user := middleware.CurrentUser(c.Request.Context())
	if !s.requireGroupMember(c, user.Id, deviceID, groupID) {
		return
	}
	state, err := s.GetGroupState(c.Request.Context(), groupID)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	if state == nil {
		c.JSON(http.StatusNotFound, errs.New("E2EE_GROUP_NOT_FOUND", "MLS group was not found.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, GroupInfoView{
		GroupId:     state.MlsGroupId,
		Epoch:       state.Epoch,
		GroupInfo:   state.GroupInfo,
		RatchetTree: state.RatchetTree,
	})
}

// POST /api/e2ee/mls/messages/fanout — SendMlsFanout (the controller forces
// the envelope type to MlsApplication).
func (s *Service) handleSendMlsFanout(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body fanoutEnvelopeBody
	if !bindJSON(c, &body) {
		return
	}
	if errs_ := validateFanoutItems(body.Payloads); errs_ != nil {
		validationError(c, errs_)
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	if body.GroupId == nil || strings.TrimSpace(*body.GroupId) == "" {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if !s.requireGroupMember(c, user.Id, deviceID, *body.GroupId) {
		return
	}
	payloads := make([]DeviceCiphertextEnvelope, 0, len(body.Payloads))
	for _, p := range body.Payloads {
		payloads = append(payloads, DeviceCiphertextEnvelope{
			RecipientDeviceID: derefOrEmpty(p.RecipientDeviceId),
			ClientMessageID:   p.ClientMessageId,
			Ciphertext:        p.Ciphertext,
			Header:            p.Header,
			Signature:         p.Signature,
			Meta:              p.Meta,
		})
	}
	result, err := s.SendFanoutEnvelopes(c.Request.Context(), user.Id, deviceID, fanoutRequest{
		RecipientAccountID: body.RecipientAccountId,
		SessionID:          body.SessionId,
		Type:               envelopeTypeMlsApplication,
		GroupID:            body.GroupId,
		ExpiresAt:          timeOf(body.ExpiresAt),
		IncludeSenderCopy:  body.IncludeSenderCopy,
		Payloads:           payloads,
	})
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/e2ee/mls/groups/:groupId/commit/fanout — FanoutMlsCommit (strict
// epoch+1 validation, then fanout, then the group commit).
func (s *Service) handleFanoutMlsCommit(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body fanoutMlsCommitBody
	if !bindJSON(c, &body) {
		return
	}
	if body.Ciphertext == nil {
		validationError(c, map[string][]string{"ciphertext": {"The Ciphertext field is required."}})
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	groupID := c.Param("groupId")
	user := middleware.CurrentUser(c.Request.Context())
	if !s.requireGroupMember(c, user.Id, deviceID, groupID) {
		return
	}
	currentState, err := s.GetGroupState(c.Request.Context(), groupID)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	if currentState != nil && body.Epoch <= currentState.Epoch {
		e := errs.New("E2EE_MLS_STALE_EPOCH", "Stale epoch for MLS commit.", http.StatusConflict)
		detail := fmt.Sprintf("Current epoch: %d, requested epoch: %d", currentState.Epoch, body.Epoch)
		e.Detail = &detail
		c.JSON(http.StatusConflict, e)
		return
	}
	if currentState != nil && body.Epoch != currentState.Epoch+1 {
		e := errs.New("E2EE_MLS_EPOCH_MISMATCH", "Epoch mismatch for MLS commit.", http.StatusConflict)
		detail := fmt.Sprintf("Current epoch: %d, requested epoch: %d", currentState.Epoch, body.Epoch)
		e.Detail = &detail
		c.JSON(http.StatusConflict, e)
		return
	}
	envelopes, err := s.FanoutMlsCommit(c.Request.Context(), user.Id, deviceID, groupID,
		body.Ciphertext, body.Header, body.Signature, body.ClientMessageId, body.Meta)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	reason := commitReason(body.Meta, "member_add")
	state, err := s.CommitMlsGroup(c.Request.Context(), user.Id, groupID, body.Epoch, reason, body.Meta)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	if state == nil {
		c.JSON(http.StatusNotFound, errs.New("E2EE_GROUP_NOT_FOUND", "MLS group was not found.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, envelopes)
}

// POST /api/e2ee/mls/groups/:groupId/messages/fanout —
// FanoutMlsMessageToGroup.
func (s *Service) handleFanoutMlsMessageToGroup(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body fanoutMlsGroupMessageBody
	if !bindJSON(c, &body) {
		return
	}
	if body.Ciphertext == nil {
		validationError(c, map[string][]string{"ciphertext": {"The Ciphertext field is required."}})
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	groupID := c.Param("groupId")
	user := middleware.CurrentUser(c.Request.Context())
	if !s.requireGroupMember(c, user.Id, deviceID, groupID) {
		return
	}
	result, err := s.FanoutMlsMessageToGroup(c.Request.Context(), user.Id, deviceID, groupID,
		body.Ciphertext, body.Header, body.Signature, body.ClientMessageId, body.Meta, envelopeTypeMlsApplication)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// GET /api/e2ee/mls/envelopes/pending — GetMlsPendingByDevice (take clamped
// to 1..500).
func (s *Service) handleGetMlsPendingByDevice(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	take := 100
	if raw := c.Query("take"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			take = parsed
		}
	}
	if take < 1 {
		take = 1
	}
	if take > 500 {
		take = 500
	}
	user := middleware.CurrentUser(c.Request.Context())
	result, err := s.GetPendingEnvelopesByDevice(c.Request.Context(), user.Id, deviceID, take)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/e2ee/mls/envelopes/:envelopeId/ack — AckMlsEnvelope.
func (s *Service) handleAckMlsEnvelope(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	deviceID, ok := requireDeviceID(c)
	if !ok {
		return
	}
	envelopeID, ok := parseRouteUUID(c, c.Param("envelopeId"))
	if !ok {
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	envelope, err := s.AckEnvelopeByDevice(c.Request.Context(), user.Id, deviceID, envelopeID)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	if envelope == nil {
		c.JSON(http.StatusNotFound, errs.New("E2EE_ENVELOPE_NOT_FOUND", "Envelope was not found.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, envelope)
}

// POST /api/e2ee/mls/devices/:deviceId/revoke — RevokeMlsDevice.
func (s *Service) handleRevokeMlsDevice(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	revoked, err := s.RevokeDevice(c.Request.Context(), user.Id, c.Param("deviceId"))
	if err != nil {
		s.serviceError(c, err)
		return
	}
	if !revoked {
		c.JSON(http.StatusNotFound, errs.New("E2EE_DEVICE_NOT_FOUND", "Device was not found.", http.StatusNotFound))
		return
	}
	c.Status(http.StatusNoContent)
}

// POST /api/e2ee/mls/devices/:deviceId/membership — AddMlsDeviceMembership.
func (s *Service) handleAddMlsDeviceMembership(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body addMlsDeviceMembershipBody
	if !bindJSON(c, &body) {
		return
	}
	if body.GroupId == "" {
		validationError(c, map[string][]string{"groupId": {"The GroupId field is required."}})
		return
	}
	user := middleware.CurrentUser(c.Request.Context())
	result, err := s.AddMlsDeviceMembership(c.Request.Context(), user.Id, c.Param("deviceId"), body.GroupId, body.Epoch)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// POST /api/e2ee/mls/groups/:groupId/reset — ResetMlsGroup.
func (s *Service) handleResetMlsGroup(c *gin.Context) {
	if !requireUser(c, false) {
		return
	}
	var body resetMlsGroupBody
	if !bindJSON(c, &body) {
		return
	}
	result, err := s.ResetMlsGroup(c.Request.Context(), c.Param("groupId"), body.NewEpoch, body.StateVersion, body.Reason)
	if err != nil {
		s.serviceError(c, err)
		return
	}
	if result == nil {
		c.JSON(http.StatusNotFound, errs.New("E2EE_GROUP_NOT_FOUND", "MLS group was not found.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, result)
}

// --- validation helpers (ASP.NET data-annotation equivalents) ---

func validatePublishKeyPackage(body *publishMlsKeyPackageBody) map[string][]string {
	var errs_ map[string][]string
	if body.KeyPackage == nil {
		errs_ = addFieldError(errs_, "keyPackage", "The KeyPackage field is required.")
	}
	if body.DeviceId == "" {
		errs_ = addFieldError(errs_, "deviceId", "The DeviceId field is required.")
	}
	if len(body.DeviceId) > 1024 {
		errs_ = addFieldError(errs_, "deviceId", "The field DeviceId must be a string or array type with a maximum length of '1024'.")
	}
	if len(body.Ciphersuite) > 128 {
		errs_ = addFieldError(errs_, "ciphersuite", "The field Ciphersuite must be a string or array type with a maximum length of '128'.")
	}
	if body.DeviceLabel != nil && len(*body.DeviceLabel) > 1024 {
		errs_ = addFieldError(errs_, "deviceLabel", "The field DeviceLabel must be a string or array type with a maximum length of '1024'.")
	}
	return errs_
}

func validateBatchCheck(body *batchCheckMlsReadyRequest) map[string][]string {
	var errs_ map[string][]string
	if len(body.AccountIds) == 0 {
		errs_ = addFieldError(errs_, "accountIds", "The field AccountIds must be a string or array type with a minimum length of '1'.")
	}
	if len(body.AccountIds) > 100 {
		errs_ = addFieldError(errs_, "accountIds", "The field AccountIds must be a string or array type with a maximum length of '100'.")
	}
	return errs_
}

func addFieldError(m map[string][]string, field, message string) map[string][]string {
	if m == nil {
		m = map[string][]string{}
	}
	m[field] = append(m[field], message)
	return m
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func timeOf(t *model.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.Time()
	return &v
}

func commitReason(meta map[string]any, fallback string) string {
	if meta != nil {
		if reason, ok := meta["reason"]; ok && reason != nil {
			s := fmt.Sprint(reason)
			if strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return fallback
}

// validateFanoutItems mirrors the [Required][MinLength(1)] + item MaxLength
// annotations on the fanout payload lists.
func validateFanoutItems(items []fanoutEnvelopeItemBody) map[string][]string {
	var errs_ map[string][]string
	if len(items) == 0 {
		errs_ = addFieldError(errs_, "payloads", "The field Payloads must be a string or array type with a minimum length of '1'.")
	}
	for i, item := range items {
		if item.RecipientDeviceId != nil && len(*item.RecipientDeviceId) > 512 {
			errs_ = addFieldError(errs_, fmt.Sprintf("payloads[%d].recipientDeviceId", i), "The field RecipientDeviceId must be a string or array type with a maximum length of '512'.")
		}
		if item.ClientMessageId != nil && len(*item.ClientMessageId) > 128 {
			errs_ = addFieldError(errs_, fmt.Sprintf("payloads[%d].clientMessageId", i), "The field ClientMessageId must be a string or array type with a maximum length of '128'.")
		}
	}
	return errs_
}
