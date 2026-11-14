package analytics

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/ports"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/authn"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/module"
)

// analyticsPort implements ports.Analytics for sibling modules.
type analyticsPort struct{ h *handler }

func (p *analyticsPort) Record(_ context.Context, event ports.AnalyticsEvent) error {
	return p.h.enqueue(event)
}

func (p *analyticsPort) Summary(ctx context.Context, kind, resourceID string, days int) (*model.AnalyticsSummary, error) {
	return p.h.summary(ctx, kind, resourceID, days)
}

// registerRoutes wires the generic ingest endpoint. Reads stay with the module
// that owns the resource, which is the only place that can check ownership.
func (h *handler) registerRoutes(host *module.Host[core.Services]) {
	host.API.POST("/analytics/events", h.ingestEvent)
}

// ingestEventRequest is the wire shape of POST /analytics/events.
type ingestEventRequest struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resource_id"`
	Event      string `json:"event"`
	Referrer   string `json:"referrer"`
}

// ingestEvent records one client-side event for any resource kind. Visitor
// identity (account, visitor cookie, address, user agent) comes from the
// request itself.
func (h *handler) ingestEvent(c *gin.Context) {
	var request ingestEventRequest
	if !httpx.BindJSON(c, &request) {
		return
	}
	visitor := ports.VisitorFromRequest(c, authn.AccountID(c), h.visitorOptions())
	if referrer := strings.TrimSpace(request.Referrer); referrer != "" {
		visitor.Referrer = referrer
	}
	event := ports.AnalyticsEvent{
		Kind:       request.Kind,
		ResourceID: request.ResourceID,
		Event:      request.Event,
		Visitor:    visitor,
	}
	if err := h.enqueue(event); err != nil {
		httpx.BadRequest(c, "ANALYTICS_EVENT_INVALID", err.Error())
		return
	}
	c.Status(http.StatusAccepted)
}
