package routeinfo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"srv.solsynth.dev/sosys/blade/config"
	"srv.solsynth.dev/sosys/blade/discovery"
)

func TestBuildResolvesAliasesThroughRegistry(t *testing.T) {
	registry := discovery.NewRegistry(nil, "test:discovery", 0, map[string]string{
		"passport": "stargate",
		"dist":     "express",
	})
	cfg := &config.Config{
		Routes: []config.RouteRule{
			{Path: "/.well-known/badges", Service: "passport", Target: "/.well-known/badges", Prefix: true},
			{Path: "/wopi", Service: "drive", Target: "/wopi"},
		},
	}

	document := Build(cfg, registry)

	if document.Counts.Aliases != 2 || document.Counts.Routes != 2 {
		t.Fatalf("counts = %+v, want 2 aliases and 2 routes", document.Counts)
	}
	if document.Aliases["dist"] != "express" {
		t.Fatalf("aliases = %+v, want dist -> express", document.Aliases)
	}
	if got := document.Routes[0]; got.Service != "passport" || got.ResolvedService != "stargate" || !got.Prefix || got.Target != "/.well-known/badges" {
		t.Fatalf("route = %+v, want passport resolved to stargate", got)
	}
	if got := document.Routes[1].ResolvedService; got != "drive" {
		t.Fatalf("route service = %q, want drive", got)
	}
}

func TestBuildUsesConfiguredAliasesWithoutRegistry(t *testing.T) {
	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{Aliases: map[string]string{"passport": "stargate"}},
		Routes:    []config.RouteRule{{Path: "/.well-known/badges", Service: "Passport"}},
	}

	document := Build(cfg, nil)

	if document.Counts.Aliases != 1 || document.Counts.Routes != 1 {
		t.Fatalf("counts = %+v, want 1 alias and 1 route", document.Counts)
	}
	if got := document.Routes[0].ResolvedService; got != "stargate" {
		t.Fatalf("resolved service = %q, want stargate", got)
	}
}

func TestHandlerServesJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Routes: []config.RouteRule{{Path: "/wopi", Service: "drive"}}}
	router := gin.New()
	router.GET("/.well-known/routes", Handler(cfg, nil))

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/.well-known/routes", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var document Document
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if document.Counts.Routes != 1 || len(document.Routes) != 1 || document.Routes[0].ResolvedService != "drive" {
		t.Fatalf("document = %+v, want the drive route", document)
	}
}
