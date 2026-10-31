// Package nearbyctl ports Passport's NearbyController HTTP surface:
// POST /api/nearby/presence-tokens and POST /api/nearby/resolve.
package nearbyctl

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"src.solsynth.dev/sosys/go/pkg/errs"

	"src.solsynth.dev/sosys/stargate/middleware"
	"src.solsynth.dev/sosys/stargate/model"
	"src.solsynth.dev/sosys/stargate/nearby"
	"src.solsynth.dev/sosys/stargate/permission"
)

// Deps is the dependency set the controller needs.
type Deps struct {
	Service *nearby.Service
	Perm    middleware.PermissionChecker
	Log     *slog.Logger
}

// Register mounts /api/nearby.
func Register(api *gin.RouterGroup, d Deps) {
	group := api.Group("/nearby")
	group.POST("/presence-tokens",
		middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.NearbyPresenceManage),
		d.issuePresenceTokens)
	group.POST("/resolve",
		middleware.RequireAuth(),
		middleware.AskPermission(d.Perm, permission.NearbyResolve),
		d.resolve)
}

type presenceTokensRequest struct {
	DeviceID      string `json:"device_id"`
	Discoverable  bool   `json:"discoverable"`
	FriendOnly    bool   `json:"friend_only"`
	Capabilities  int    `json:"capabilities"`
	PrefetchSlots int    `json:"prefetch_slots"`
}

type presenceTokenDTO struct {
	Slot      int64      `json:"slot"`
	Token     string     `json:"token"`
	ValidFrom model.Time `json:"valid_from"`
	ValidTo   model.Time `json:"valid_to"`
}

type presenceTokensResponse struct {
	ServiceUUID     string             `json:"service_uuid"`
	SlotDurationSec int                `json:"slot_duration_sec"`
	Tokens          []presenceTokenDTO `json:"tokens"`
}

type resolveObservationRequest struct {
	Token       string      `json:"token"`
	Slot        int64       `json:"slot"`
	AvgRSSI     int         `json:"avg_rssi"`
	SeenCount   int         `json:"seen_count"`
	DurationMS  int64       `json:"duration_ms"`
	FirstSeenAt *model.Time `json:"first_seen_at"`
	LastSeenAt  *model.Time `json:"last_seen_at"`
}

type resolveRequest struct {
	Observations []resolveObservationRequest `json:"observations"`
}

type resolvePeerResponse struct {
	UserID      string                            `json:"user_id"`
	DisplayName string                            `json:"display_name"`
	Avatar      *model.SnCloudFileReferenceObject `json:"avatar"`
	IsFriend    bool                              `json:"is_friend"`
	CanInvite   bool                              `json:"can_invite"`
	Visibility  string                            `json:"visibility"`
	LastSeenAt  model.Time                        `json:"last_seen_at"`
}

type resolveResponse struct {
	Peers []resolvePeerResponse `json:"peers"`
}

func (d Deps) issuePresenceTokens(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	request := presenceTokensRequest{
		Discoverable:  true,
		FriendOnly:    true,
		PrefetchSlots: nearby.DefaultPrefetchSlots,
	}
	if !bindJSON(c, &request) {
		return
	}
	deviceID := strings.TrimSpace(request.DeviceID)
	if deviceID == "" {
		validation(c, "device_id", "The DeviceId field is required.")
		return
	}
	if len(deviceID) > 256 {
		validation(c, "device_id", "The field DeviceId must be a string with a maximum length of 256.")
		return
	}
	if d.Service == nil {
		d.unavailable(c, "Nearby service is not configured.")
		return
	}

	result, err := d.Service.IssuePresenceTokens(c.Request.Context(), accountID(account), nearby.IssueRequest{
		DeviceID:      deviceID,
		Discoverable:  request.Discoverable,
		FriendOnly:    request.FriendOnly,
		Capabilities:  request.Capabilities,
		PrefetchSlots: request.PrefetchSlots,
	})
	if errors.Is(err, nearby.ErrUnavailable) {
		d.unavailable(c, "Nearby presence token secret is not configured.")
		return
	}
	if err != nil {
		d.internal(c, err)
		return
	}

	tokens := make([]presenceTokenDTO, 0, len(result.Tokens))
	for _, token := range result.Tokens {
		tokens = append(tokens, presenceTokenDTO{
			Slot:      token.Slot,
			Token:     token.Token,
			ValidFrom: *model.NewTime(time.Unix(token.ValidFrom, 0).UTC()),
			ValidTo:   *model.NewTime(time.Unix(token.ValidTo, 0).UTC()),
		})
	}
	c.JSON(http.StatusOK, presenceTokensResponse{
		ServiceUUID:     result.ServiceUUID,
		SlotDurationSec: result.SlotDurationSec,
		Tokens:          tokens,
	})
}

func (d Deps) resolve(c *gin.Context) {
	account := currentAccount(c)
	if account == nil {
		return
	}
	var request resolveRequest
	if !bindJSON(c, &request) {
		return
	}
	if len(request.Observations) == 0 {
		validation(c, "observations", "The Observations field is required.")
		return
	}
	observations := make([]nearby.Observation, 0, len(request.Observations))
	for _, item := range request.Observations {
		token := strings.TrimSpace(item.Token)
		if len(token) != nearby.TokenBytes*2 {
			validation(c, "observations", "The field Token must be a string with a minimum length of 32 and a maximum length of 32.")
			return
		}
		observations = append(observations, nearby.Observation{
			Token:       token,
			Slot:        item.Slot,
			AvgRSSI:     item.AvgRSSI,
			SeenCount:   item.SeenCount,
			DurationMS:  item.DurationMS,
			FirstSeenAt: unixTime(item.FirstSeenAt),
			LastSeenAt:  unixTime(item.LastSeenAt),
		})
	}
	if d.Service == nil {
		d.unavailable(c, "Nearby service is not configured.")
		return
	}

	peers, err := d.Service.Resolve(c.Request.Context(), accountID(account), observations)
	if errors.Is(err, nearby.ErrUnavailable) {
		d.unavailable(c, "Nearby presence token secret is not configured.")
		return
	}
	if err != nil {
		d.internal(c, err)
		return
	}
	response := resolveResponse{Peers: make([]resolvePeerResponse, 0, len(peers))}
	for _, peer := range peers {
		response.Peers = append(response.Peers, resolvePeerResponse{
			UserID:      peer.UserID,
			DisplayName: peer.DisplayName,
			Avatar:      peer.Avatar,
			IsFriend:    peer.IsFriend,
			CanInvite:   peer.CanInvite,
			Visibility:  peer.Visibility,
			LastSeenAt:  *model.NewTime(time.Unix(peer.LastSeenAt, 0).UTC()),
		})
	}
	c.JSON(http.StatusOK, response)
}

// ─────────────────────────── helpers ───────────────────────────

func currentAccount(c *gin.Context) *model.Account {
	account := middleware.CurrentUser(c.Request.Context())
	if account == nil {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized("Authentication is required."))
		return nil
	}
	return account
}

func accountID(account *model.Account) (id uuid.UUID) {
	id, _ = uuid.Parse(account.Id)
	return id
}

func unixTime(value *model.Time) *int64 {
	if value == nil {
		return nil
	}
	seconds := value.Time().Unix()
	return &seconds
}

func validation(c *gin.Context, field, message string) {
	c.JSON(http.StatusBadRequest, errs.Validation(map[string][]string{field: {message}}))
}

func bindJSON(c *gin.Context, target any) bool {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		validation(c, "body", "Invalid request body.")
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := json.Unmarshal(body, target); err != nil {
		validation(c, "body", "Invalid request body.")
		return false
	}
	return true
}

func (d Deps) internal(c *gin.Context, err error) {
	if d.Log != nil {
		d.Log.Error("nearby request failed", "path", c.FullPath(), "error", err)
	}
	c.JSON(http.StatusInternalServerError, errs.New("INTERNAL_ERROR", "Internal server error.", http.StatusInternalServerError))
}

func (d Deps) unavailable(c *gin.Context, message string) {
	c.JSON(http.StatusServiceUnavailable, errs.New("SERVICE_UNAVAILABLE", message, http.StatusServiceUnavailable))
}
