package authctl

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/middleware"
	"src.solsynth.dev/sosys/stargate/internal/model"
)

// QrLoginController port. Redis keys use the RAW go-redis client
// (auth:qr:{id}, 5min TTL) per the plan contract; the C# ICacheService
// envelope is not used here.

const (
	qrCachePrefix  = "auth:qr:"
	qrToAuthPrefix = "auth:qr:auth:"
	qrChallengeTTL = 5 * time.Minute
)

// qrLoginStatus mirrors QrLoginStatus (Pending=0, Scanned=1, Approved=2,
// Declined=3, Expired=4).
type qrLoginStatus int

const (
	qrStatusPending  qrLoginStatus = 0
	qrStatusScanned  qrLoginStatus = 1
	qrStatusApproved qrLoginStatus = 2
	qrStatusDeclined qrLoginStatus = 3
	qrStatusExpired  qrLoginStatus = 4
)

// qrLoginChallenge mirrors the QrLoginChallenge record stored in Redis.
type qrLoginChallenge struct {
	Id                  string               `json:"id"`
	AuthChallengeId     string               `json:"auth_challenge_id"`
	AccountId           string               `json:"account_id"`
	DeviceId            string               `json:"device_id"`
	DeviceName          *string              `json:"device_name,omitempty"`
	Platform            model.ClientPlatform `json:"platform"`
	Status              qrLoginStatus        `json:"status"`
	CreatedAt           *model.Time          `json:"created_at,omitempty"`
	ExpiresAt           *model.Time          `json:"expires_at,omitempty"`
	ApprovedAt          *model.Time          `json:"approved_at,omitempty"`
	ApprovedBySessionId *string              `json:"approved_by_session_id,omitempty"`
	ApprovedDeviceId    *string              `json:"approved_device_id,omitempty"`
}

// qrGenerateRequest mirrors QrGenerateRequest.
type qrGenerateRequest struct {
	DeviceId   string               `json:"device_id"`
	DeviceName *string              `json:"device_name"`
	Platform   model.ClientPlatform `json:"platform"`
	Audiences  []string             `json:"audiences"`
	Scopes     []string             `json:"scopes"`
}

// qrGenerateResponse mirrors QrGenerateResponse.
type qrGenerateResponse struct {
	QrChallengeId    string      `json:"qr_challenge_id"`
	AuthChallengeId  string      `json:"auth_challenge_id"`
	QrData           string      `json:"qr_data"`
	ExpiresAt        *model.Time `json:"expires_at"`
	ExpiresInSeconds int         `json:"expires_in_seconds"`
}

// qrStatusResponse mirrors QrStatusResponse.
type qrStatusResponse struct {
	QrChallengeId    string               `json:"qr_challenge_id"`
	AuthChallengeId  string               `json:"auth_challenge_id"`
	Status           int                  `json:"status"`
	ExpiresAt        *model.Time          `json:"expires_at"`
	ApprovedAt       *model.Time          `json:"approved_at,omitempty"`
	ApprovedDeviceId *string              `json:"approved_device_id,omitempty"`
	DeviceName       *string              `json:"device_name,omitempty"`
	Platform         model.ClientPlatform `json:"platform"`
}

func (h *handler) generateQrChallenge(c *gin.Context) {
	if !h.requireCache(c) {
		return
	}
	ctx := c.Request.Context()
	var req qrGenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("BAD_REQUEST", "Invalid request body."))
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(qrChallengeTTL)
	ipAddress := middleware.ClientIP(c.Request)
	userAgent := c.Request.UserAgent()
	deviceName := userAgent
	if req.DeviceName != nil {
		deviceName = *req.DeviceName
	}

	authChallenge := &model.AuthChallenge{
		Id:         uuid.NewString(),
		StepTotal:  1,
		StepRemain: 1,
		DeviceId:   req.DeviceId,
		DeviceName: &deviceName,
		Platform:   req.Platform,
		IpAddress:  &ipAddress,
		UserAgent:  &userAgent,
		Audiences:  req.Audiences,
		Scopes:     req.Scopes,
		AccountId:  uuid.Nil.String(),
		ExpiredAt:  model.NewTime(expiresAt),
		CreatedAt:  model.NewTime(now),
		UpdatedAt:  model.NewTime(now),
	}
	if ipAddress != "" {
		authChallenge.Location = h.d.Geo.GetPointFromIp(ipAddress)
	}
	if err := h.d.Store.CreateAuthChallenge(ctx, authChallenge); err != nil {
		h.logError("create qr auth challenge", err)
		c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "An internal server error occurred.", http.StatusInternalServerError))
		return
	}

	qrID := uuid.NewString()
	qrChallenge := &qrLoginChallenge{
		Id:              qrID,
		AuthChallengeId: authChallenge.Id,
		AccountId:       uuid.Nil.String(),
		DeviceId:        req.DeviceId,
		DeviceName:      req.DeviceName,
		Platform:        req.Platform,
		Status:          qrStatusPending,
		CreatedAt:       model.NewTime(now),
		ExpiresAt:       model.NewTime(expiresAt),
	}
	if err := h.setQrChallenge(ctx, qrID, qrChallenge, qrChallengeTTL); err != nil {
		h.logError("store qr challenge", err)
	}
	_ = h.d.Redis.Raw.Set(ctx, qrToAuthPrefix+authChallenge.Id, qrID, qrChallengeTTL).Err()

	c.JSON(http.StatusOK, qrGenerateResponse{
		QrChallengeId:    qrID,
		AuthChallengeId:  authChallenge.Id,
		QrData:           "solian://auth/qr/" + qrID,
		ExpiresAt:        model.NewTime(expiresAt),
		ExpiresInSeconds: int(qrChallengeTTL.Seconds()),
	})
}

func (h *handler) getQrStatus(c *gin.Context) {
	if !h.requireCache(c) {
		return
	}
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("PADLOCK_QR_CHALLENGE_NOT_FOUND", "QR challenge not found or expired.", http.StatusNotFound))
		return
	}
	qr, err := h.getQrChallenge(ctx, id.String())
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("PADLOCK_QR_CHALLENGE_NOT_FOUND", "QR challenge not found or expired.", http.StatusNotFound))
		return
	}
	c.JSON(http.StatusOK, qrStatusResponse{
		QrChallengeId:    qr.Id,
		AuthChallengeId:  qr.AuthChallengeId,
		Status:           int(qr.Status),
		ExpiresAt:        qr.ExpiresAt,
		ApprovedAt:       qr.ApprovedAt,
		ApprovedDeviceId: qr.ApprovedDeviceId,
		DeviceName:       qr.DeviceName,
		Platform:         qr.Platform,
	})
}

func (h *handler) scanQrChallenge(c *gin.Context) {
	if !h.requireCache(c) {
		return
	}
	ctx := c.Request.Context()
	user := middleware.CurrentUser(ctx)
	if user == nil {
		c.JSON(http.StatusUnauthorized, errs.New("UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("PADLOCK_QR_CHALLENGE_NOT_FOUND", "QR challenge not found or expired.", http.StatusNotFound))
		return
	}
	key := qrCachePrefix + id.String()
	qr, err := h.getQrChallenge(ctx, id.String())
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("PADLOCK_QR_CHALLENGE_NOT_FOUND", "QR challenge not found or expired.", http.StatusNotFound))
		return
	}
	if qr.Status != qrStatusPending {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_QR_CHALLENGE_NOT_PENDING", "QR challenge is no longer pending."))
		return
	}
	now := time.Now().UTC()
	if qr.ExpiresAt != nil && now.After(qr.ExpiresAt.Time()) {
		_ = h.d.Redis.Raw.Del(ctx, key).Err()
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_QR_CHALLENGE_EXPIRED", "QR challenge has expired."))
		return
	}

	scanned := *qr
	scanned.Status = qrStatusScanned
	ttl := time.Until(qr.ExpiresAt.Time())
	if err := h.setQrChallenge(ctx, id.String(), &scanned, ttl); err != nil {
		h.logError("update qr challenge", err)
	}

	var scannedByDevice *string
	if session := middleware.CurrentSession(ctx); session != nil {
		scannedByDevice = &session.Id
	}
	h.publishWS(ctx, user.Id, "auth.qr.scanned", map[string]any{
		"qr_challenge_id":   qr.Id,
		"scanned_by_device": scannedByDevice,
	})
	c.Status(http.StatusOK)
}

func (h *handler) approveQrChallenge(c *gin.Context) {
	if !h.requireCache(c) {
		return
	}
	ctx := c.Request.Context()
	user := middleware.CurrentUser(ctx)
	session := middleware.CurrentSession(ctx)
	if user == nil || session == nil {
		c.JSON(http.StatusUnauthorized, errs.New("UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	hasQrFactor, err := h.d.Store.HasEnabledFactor(ctx, user.Id, model.AuthFactorTypeQrLogin)
	if err != nil {
		h.logError("check qr factor", err)
	}
	if !hasQrFactor {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_QR_FACTOR_NOT_ENABLED", "QR login factor is not enabled for this account."))
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("PADLOCK_QR_CHALLENGE_NOT_FOUND", "QR challenge not found or expired.", http.StatusNotFound))
		return
	}
	key := qrCachePrefix + id.String()
	qr, err := h.getQrChallenge(ctx, id.String())
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("PADLOCK_QR_CHALLENGE_NOT_FOUND", "QR challenge not found or expired.", http.StatusNotFound))
		return
	}
	if qr.Status != qrStatusPending && qr.Status != qrStatusScanned {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_QR_CHALLENGE_NOT_PENDING", "QR challenge is no longer pending."))
		return
	}
	now := time.Now().UTC()
	if qr.ExpiresAt != nil && now.After(qr.ExpiresAt.Time()) {
		_ = h.d.Redis.Raw.Del(ctx, key).Err()
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_QR_CHALLENGE_EXPIRED", "QR challenge has expired."))
		return
	}

	authChallenge, err := h.d.Store.GetAuthChallenge(ctx, uuid.MustParse(qr.AuthChallengeId))
	if err != nil {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_AUTH_CHALLENGE_NOT_FOUND", "Associated auth challenge not found."))
		return
	}
	authChallenge.AccountId = user.Id
	authChallenge.StepRemain = 0
	authChallenge.ApprovedAt = model.NewTime(now)
	authChallenge.ApprovedBySessionId = &session.Id
	authChallenge.UpdatedAt = model.NewTime(now)
	if err := h.d.Store.UpdateAuthChallenge(ctx, authChallenge); err != nil {
		h.logError("update auth challenge", err)
	}

	approved := *qr
	approved.AccountId = user.Id
	approved.Status = qrStatusApproved
	approved.ApprovedAt = model.NewTime(now)
	approved.ApprovedBySessionId = &session.Id
	if session.ClientId != nil {
		approved.ApprovedDeviceId = session.ClientId
	}
	ttl := time.Until(qr.ExpiresAt.Time())
	if err := h.setQrChallenge(ctx, id.String(), &approved, ttl); err != nil {
		h.logError("update qr challenge", err)
	}

	h.publishWS(ctx, user.Id, "auth.qr.approved", map[string]any{
		"qr_challenge_id":    qr.Id,
		"auth_challenge_id":  authChallenge.Id,
		"approved_by_device": session.Id,
	})
	c.Status(http.StatusOK)
}

func (h *handler) declineQrChallenge(c *gin.Context) {
	if !h.requireCache(c) {
		return
	}
	ctx := c.Request.Context()
	user := middleware.CurrentUser(ctx)
	session := middleware.CurrentSession(ctx)
	if user == nil || session == nil {
		c.JSON(http.StatusUnauthorized, errs.New("UNAUTHORIZED", "Authentication is required.", http.StatusUnauthorized))
		return
	}
	hasQrFactor, err := h.d.Store.HasEnabledFactor(ctx, user.Id, model.AuthFactorTypeQrLogin)
	if err != nil {
		h.logError("check qr factor", err)
	}
	if !hasQrFactor {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_QR_FACTOR_NOT_ENABLED", "QR login factor is not enabled for this account."))
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		c.JSON(http.StatusNotFound, errs.New("PADLOCK_QR_CHALLENGE_NOT_FOUND", "QR challenge not found or expired.", http.StatusNotFound))
		return
	}
	key := qrCachePrefix + id.String()
	qr, err := h.getQrChallenge(ctx, id.String())
	if err != nil {
		c.JSON(http.StatusNotFound, errs.New("PADLOCK_QR_CHALLENGE_NOT_FOUND", "QR challenge not found or expired.", http.StatusNotFound))
		return
	}
	if qr.Status != qrStatusPending && qr.Status != qrStatusScanned {
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_QR_CHALLENGE_NOT_PENDING", "QR challenge is no longer pending."))
		return
	}
	now := time.Now().UTC()
	if qr.ExpiresAt != nil && now.After(qr.ExpiresAt.Time()) {
		_ = h.d.Redis.Raw.Del(ctx, key).Err()
		c.JSON(http.StatusBadRequest, errs.BadRequest("PADLOCK_QR_CHALLENGE_EXPIRED", "QR challenge has expired."))
		return
	}

	declined := *qr
	declined.Status = qrStatusDeclined
	declined.ApprovedAt = model.NewTime(now)
	declined.ApprovedBySessionId = &session.Id
	ttl := time.Until(qr.ExpiresAt.Time())
	if err := h.setQrChallenge(ctx, id.String(), &declined, ttl); err != nil {
		h.logError("update qr challenge", err)
	}

	h.publishWS(ctx, user.Id, "auth.qr.declined", map[string]any{
		"qr_challenge_id":    qr.Id,
		"declined_by_device": session.Id,
	})
	c.Status(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Redis helpers (RAW go-redis, mirroring the C# ICacheService keys)
// ---------------------------------------------------------------------------

func (h *handler) setQrChallenge(ctx context.Context, id string, qr *qrLoginChallenge, ttl time.Duration) error {
	data, err := json.Marshal(qr)
	if err != nil {
		return err
	}
	return h.d.Redis.Raw.Set(ctx, qrCachePrefix+id, data, ttl).Err()
}

func (h *handler) getQrChallenge(ctx context.Context, id string) (*qrLoginChallenge, error) {
	raw, err := h.d.Redis.Raw.Get(ctx, qrCachePrefix+id).Bytes()
	if err != nil {
		return nil, err
	}
	var qr qrLoginChallenge
	if err := json.Unmarshal(raw, &qr); err != nil {
		return nil, err
	}
	return &qr, nil
}
