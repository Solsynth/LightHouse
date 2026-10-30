package wsgateway

import (
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"srv.solsynth.dev/sosys/blade/internal/logging"
	dyauth "src.solsynth.dev/sosys/go/pkg/auth"
	"src.solsynth.dev/sosys/go/pkg/cache"
	gen "src.solsynth.dev/sosys/go/proto"
)

type HttpHandler struct {
	authenticator dyauth.TokenAuthenticator
	service       *Service
	cfg           Config
	cache         cache.CacheService
	profiles      gen.DyProfileServiceClient
}

func NewHttpHandler(authenticator dyauth.TokenAuthenticator, service *Service, cfg Config, c cache.CacheService, profiles gen.DyProfileServiceClient) *HttpHandler {
	if cfg.KeepAliveInterval <= 0 {
		cfg.KeepAliveInterval = 60 * time.Second
	}

	return &HttpHandler{
		authenticator: authenticator,
		service:       service,
		cfg:           cfg,
		cache:         c,
		profiles:      profiles,
	}
}

func (h *HttpHandler) Handle(c *gin.Context) {
	requestPath := c.Request.URL.Path
	requestQuery := c.Request.URL.RawQuery
	requestOrigin := c.Request.Header.Get("Origin")

	namespace := c.Query("namespace")
	if namespace == "" {
		namespace = h.cfg.DefaultNamespace
	}

	auth, err := dyauth.AuthenticateRequest(c.Request.Context(), h.authenticator, c.Request)
	if err != nil {
		logging.Log.Warn().
			Err(err).
			Str("path", requestPath).
			Str("query", requestQuery).
			Str("origin", requestOrigin).
			Str("namespace", namespace).
			Msg("Rejected websocket request due to authentication failure")
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	tokenInfo, _ := dyauth.ExtractToken(c.Request)

	// Hydrate profile and touch last-seen (best-effort)
	_ = dyauth.HydrateAndTouch(c.Request.Context(), h.cache, h.profiles, auth)

	deviceID := auth.Session.GetClientId()

	// The gateway already allows every origin through CORS, so the handshake
	// keeps accepting cross-origin browsers and clients that omit Origin. A
	// present Origin must still parse, as it did under x/net's handshake check.
	if requestOrigin != "" {
		if _, err := url.ParseRequestURI(requestOrigin); err != nil {
			logging.Log.Warn().
				Err(err).
				Str("path", requestPath).
				Str("origin", requestOrigin).
				Str("namespace", namespace).
				Str("accountId", auth.Account.GetId()).
				Str("deviceId", deviceID).
				Msg("Rejected websocket handshake due to invalid origin")
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid websocket origin"})
			return
		}
	}

	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote the HTTP error response.
		logging.Log.Warn().
			Err(err).
			Str("path", requestPath).
			Str("origin", requestOrigin).
			Str("namespace", namespace).
			Str("accountId", auth.Account.GetId()).
			Str("deviceId", deviceID).
			Msg("Failed to upgrade websocket connection")
		return
	}

	logging.Log.Info().
		Str("namespace", namespace).
		Str("accountId", auth.Account.GetId()).
		Str("deviceId", deviceID).
		Str("sessionId", auth.Session.GetId()).
		Str("origin", requestOrigin).
		Str("path", requestPath).
		Msg("Upgraded websocket connection")

	h.service.HandleConnection(c.Request.Context(), SessionAuthContext{
		Account:   auth.Account,
		Session:   auth.Session,
		TokenInfo: tokenInfo,
		Request:   dyauth.CloneRequestMetadata(c.Request),
	}, namespace, deviceID, conn)

	logging.Log.Debug().
		Str("namespace", namespace).
		Str("accountId", auth.Account.GetId()).
		Str("deviceId", deviceID).
		Msg("Websocket handler completed")
}
