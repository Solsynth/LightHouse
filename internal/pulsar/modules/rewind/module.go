package rewind

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"

	"src.solsynth.dev/sosys/lighthouse/internal/pulsar/core"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// ServiceID identifies Pulsar in the yearly Rewind aggregation.
const ServiceID = "pulsar"

func Factory() module.Factory[core.Services] {
	return module.Factory[core.Services]{
		ID:             "app.solian.pulsar.rewind",
		Kind:           module.KindFeature,
		DefaultEnabled: true,
		Capabilities:   []string{"rewind", "stats"},
		New:            func() module.Instance[core.Services] { return &instance{} },
	}
}

type instance struct {
	core *core.Services
}

func (i *instance) Configure(_ context.Context, host *module.Host[core.Services]) error {
	i.core = host.Core

	// HTTP routes
	host.API.GET("/stats", authn.RequireAuth(), i.myStats)
	host.API.GET("/users/:identifier/stats", i.userStats)
	host.API.GET("/rewind/:year", authn.RequireAuth(), i.myRewind)
	host.API.GET("/users/:identifier/rewind/:year", i.userRewind)

	// gRPC DyRewindServiceServer
	gen.RegisterDyRewindServiceServer(host.GRPC, &server{core: host.Core})

	return nil
}

func (i *instance) Stop(context.Context) error { return nil }

func (i *instance) myStats(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	i.renderStats(c, accountID)
}

func (i *instance) userStats(c *gin.Context) {
	identifier := c.Param("identifier")
	accountID, err := i.core.ResolveAccount(c.Request.Context(), identifier)
	if err != nil {
		httpx.NotFound(c, "user not found")
		return
	}
	i.renderStats(c, accountID)
}

func (i *instance) renderStats(c *gin.Context, accountID uuid.UUID) {
	domain := c.Query("domain")
	var fromTime, toTime *time.Time
	if fromStr := c.Query("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			fromTime = &t
		}
	}
	if toStr := c.Query("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			toTime = &t
		}
	}

	stats, err := i.core.Store.GetMediaStats(c.Request.Context(), accountID, domain, fromTime, toTime)
	if err != nil {
		httpx.Internal(c)
		return
	}
	httpx.OK(c, stats)
}

func (i *instance) myRewind(c *gin.Context) {
	accountIDStr := authn.AccountID(c)
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		httpx.Unauthorized(c)
		return
	}
	i.renderRewind(c, accountID)
}

func (i *instance) userRewind(c *gin.Context) {
	identifier := c.Param("identifier")
	accountID, err := i.core.ResolveAccount(c.Request.Context(), identifier)
	if err != nil {
		httpx.NotFound(c, "user not found")
		return
	}
	i.renderRewind(c, accountID)
}

func (i *instance) renderRewind(c *gin.Context, accountID uuid.UUID) {
	yearStr := c.Param("year")
	year, err := strconv.Atoi(yearStr)
	if err != nil || year < 2000 || year > 2100 {
		httpx.BadRequest(c, "INVALID_YEAR", "invalid year")
		return
	}

	start, end := yearWindow(year)
	stats, err := i.core.Store.GetMediaStats(c.Request.Context(), accountID, "", &start, &end)
	if err != nil {
		httpx.Internal(c)
		return
	}

	httpx.OK(c, gin.H{
		"year":       year,
		"account_id": accountID,
		"rewind":     stats,
	})
}

// ─────────────────────────── gRPC DyRewindService ───────────────────────────

type server struct {
	gen.UnimplementedDyRewindServiceServer
	core *core.Services
}

// yearWindow mirrors the standard fleet rewind aggregation window: [year-1-12-26, year-12-26).
func yearWindow(year int) (time.Time, time.Time) {
	start := time.Date(year-1, time.December, 26, 0, 0, 0, 0, time.UTC)
	end := time.Date(year, time.December, 26, 0, 0, 0, 0, time.UTC)
	return start, end
}

func (s *server) GetRewindEvent(ctx context.Context, req *gen.DyRequestRewindEvent) (*gen.DyRewindEvent, error) {
	accountIDStr := strings.TrimSpace(req.GetAccountId())
	if accountIDStr == "" {
		return &gen.DyRewindEvent{ServiceId: ServiceID}, nil
	}
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		return &gen.DyRewindEvent{AccountId: accountIDStr, ServiceId: ServiceID}, nil
	}

	start, end := yearWindow(int(req.GetYear()))
	stats, err := s.core.Store.GetMediaStats(ctx, accountID, "", &start, &end)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(stats)
	if err != nil {
		return nil, err
	}

	return &gen.DyRewindEvent{
		AccountId: accountIDStr,
		ServiceId: ServiceID,
		Data:      raw,
	}, nil
}
