package ports

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/httpx"
)

// Analytics records resource analytics and answers aggregate queries for the
// module that owns the resource. Ownership checks stay with the owning module;
// the analytics provider only stores and aggregates.
type Analytics interface {
	// Record queues one event for batched persistence. It never blocks on the
	// database and fails only for invalid events (empty kind/resource/event).
	Record(ctx context.Context, event AnalyticsEvent) error
	// Summary aggregates events for one resource over the last days (1-365).
	Summary(ctx context.Context, kind, resourceID string, days int) (*model.AnalyticsSummary, error)
}

// AnalyticsEvent is one event recorded against a resource.
type AnalyticsEvent struct {
	// Kind is the resource type, for example "post".
	Kind string
	// ResourceID identifies the resource inside that kind.
	ResourceID string
	// Event is the event name, for example "view" or "reaction.add".
	Event string
	// Visitor is the request identity behind the event.
	Visitor AnalyticsVisitor
}

// AnalyticsVisitor is the identity behind an analytics event. AccountID wins
// when the visitor is signed in; VisitorID identifies anonymous browsers that
// carry the visitor cookie. IP is hashed by the provider before it is stored.
type AnalyticsVisitor struct {
	AccountID string
	VisitorID string
	IP        string
	UserAgent string
	Referrer  string
}

// VisitorOptions configures VisitorFromRequest.
type VisitorOptions struct {
	// CookieName names the long-lived anonymous visitor cookie.
	CookieName string
	// Secure marks the cookie Secure; enable it on TLS deployments.
	Secure bool
	// TrustedProxyHops is how many reverse proxies may be trusted when
	// resolving the client address.
	TrustedProxyHops int
}

// visitorContextKey caches the visitor for the current request so a handler
// that builds several viewers emits only one cookie.
const visitorContextKey = "analytics_visitor"

// VisitorFromRequest collects the analytics identity of a request and issues
// the visitor cookie when the client does not carry a valid one. The collected
// value is cached in the gin context.
func VisitorFromRequest(c *gin.Context, accountID string, opts VisitorOptions) AnalyticsVisitor {
	if c == nil {
		return AnalyticsVisitor{AccountID: accountID}
	}
	if cached, ok := c.Get(visitorContextKey); ok {
		if visitor, ok := cached.(AnalyticsVisitor); ok {
			return visitor
		}
	}
	visitor := AnalyticsVisitor{
		AccountID: accountID,
		UserAgent: c.Request.UserAgent(),
		Referrer:  c.Request.Referer(),
		IP:        httpx.ClientIP(c.Request, opts.TrustedProxyHops),
	}
	name := opts.CookieName
	if name == "" {
		name = "sol_visitor"
	}
	if raw, err := c.Cookie(name); err == nil {
		if id, err := uuid.Parse(raw); err == nil && id != uuid.Nil {
			visitor.VisitorID = id.String()
		}
	}
	if visitor.VisitorID == "" {
		visitor.VisitorID = uuid.NewString()
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie(name, visitor.VisitorID, 365*24*60*60, "/", "", opts.Secure, true)
	}
	c.Set(visitorContextKey, visitor)
	return visitor
}
