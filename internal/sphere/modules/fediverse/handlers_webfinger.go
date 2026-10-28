package fediverse

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/httpx"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/model"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/store"
)

// handleHostMeta serves the XRD document pointing at the WebFinger endpoint.
func (s *server) handleHostMeta(c *gin.Context) {
	webfingerURL := s.baseURL + "/.well-known/webfinger?resource={uri}"
	body := `<?xml version="1.0" encoding="UTF-8"?>
<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0">
  <Link rel="lrdd" template="` + webfingerURL + `"/>
</XRD>`
	c.Data(http.StatusOK, "application/xrd+xml", []byte(body))
}

// handleWebFinger resolves acct: resources for local publishers.
func (s *server) handleWebFinger(c *gin.Context) {
	resource := c.Query("resource")
	if resource == "" {
		httpx.BadRequest(c, "WEBFINGER_RESOURCE_REQUIRED", "Missing resource parameter")
		return
	}
	if !strings.HasPrefix(resource, "acct:") {
		httpx.BadRequest(c, "WEBFINGER_RESOURCE_INVALID_FORMAT", "Invalid resource format")
		return
	}
	account := strings.TrimPrefix(resource, "acct:")
	parts := strings.Split(account, "@")
	if len(parts) != 2 {
		httpx.BadRequest(c, "WEBFINGER_ACCOUNT_INVALID_FORMAT", "Invalid account format")
		return
	}
	username, domain := parts[0], parts[1]
	if !strings.EqualFold(domain, s.domain) {
		httpx.Fail(c, errsNotFound("WEBFINGER_DOMAIN_NOT_FOUND", "Domain not found."))
		return
	}

	if strings.EqualFold(username, "solar-network") {
		c.Header("Content-Type", "application/jrd+json; charset=utf-8")
		c.JSON(http.StatusOK, gin.H{
			"subject": resource,
			"links": []gin.H{
				{"rel": "self", "type": "application/activity+json", "href": s.baseURL + "/activitypub/actor"},
				{"rel": "http://webfinger.net/rel/profile-page", "type": "text/html", "href": s.baseURL},
			},
		})
		return
	}

	publisher, err := s.loadPublisher(c.Request.Context(), username)
	if err != nil {
		httpx.Fail(c, errsNotFound("WEBFINGER_PUBLISHER_NOT_FOUND", "Publisher not found."))
		return
	}
	if publisher.URI == nil || *publisher.URI == "" {
		httpx.Fail(c, errsNotFound("WEBFINGER_ACTOR_NOT_FOUND", "Actor not found."))
		return
	}

	c.Header("Content-Type", "application/jrd+json; charset=utf-8")
	c.JSON(http.StatusOK, gin.H{
		"subject": resource,
		"links": []gin.H{
			{"rel": "self", "type": "application/activity+json", "href": s.actorURL(publisher.Name)},
			{"rel": "http://webfinger.net/rel/profile-page", "type": "text/html", "href": s.baseURL + "/publishers/" + publisher.Name},
		},
	})
}

// handleNodeInfoDiscovery serves the NodeInfo discovery document.
func (s *server) handleNodeInfoDiscovery(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"links": []gin.H{
			{
				"rel":  "http://nodeinfo.diaspora.software/ns/schema/2.1",
				"href": s.baseURL + "/nodeinfo/2.1",
			},
			{
				"rel":  "http://nodeinfo.diaspora.software/ns/schema/2.0",
				"href": s.baseURL + "/nodeinfo/2.0",
			},
		},
	})
}

type nodeInfoStats struct {
	totalPublishers  int64
	activePublishers int64
	totalPosts       int64
}

func (s *server) nodeInfoStats(c *gin.Context) nodeInfoStats {
	ctx := c.Request.Context()
	var stats nodeInfoStats
	_ = s.db.WithContext(ctx).Model(&store.PublisherEntity{}).Count(&stats.totalPublishers).Error
	_ = s.db.WithContext(ctx).Model(&store.PublisherEntity{}).
		Where("type <> ? AND uri IS NOT NULL", int(model.PublisherFediverse)).
		Count(&stats.activePublishers).Error
	_ = s.db.WithContext(ctx).Model(&store.PostEntity{}).
		Where("publisher_id <> ?", uuidNil()).
		Count(&stats.totalPosts).Error
	return stats
}

func (s *server) handleNodeInfo20(c *gin.Context) {
	s.writeNodeInfo(c, "2.0")
}

func (s *server) handleNodeInfo21(c *gin.Context) {
	s.writeNodeInfo(c, "2.1")
}

func (s *server) writeNodeInfo(c *gin.Context, version string) {
	stats := s.nodeInfoStats(c)
	description := gin.H{
		"software": gin.H{
			"name":    "dysonnetwork",
			"version": "1.0.0",
		},
		"protocols": []string{"activitypub"},
		"services": gin.H{
			"inbound":  []string{},
			"outbound": []string{"activitypub"},
		},
		"usage": gin.H{
			"users": gin.H{
				"total":         stats.totalPublishers,
				"active_month":  stats.activePublishers,
				"active_halfyear": stats.activePublishers,
			},
			"local_posts":    stats.totalPosts,
			"local_comments": 0,
		},
		"open_registrations": true,
		"configuration": gin.H{
			"accounts": gin.H{
				"max_featured_objects": 10,
			},
			"statuses": gin.H{
				"max_characters":        5000,
				"max_media_attachments": 4,
			},
			"media_attachments": gin.H{
				"supported_mime_types":    []string{"image/*", "video/*", "audio/*"},
				"image_size_limit":        10_000_000,
				"image_matrix_limit":      4_000_000,
				"video_size_limit":        40_000_000,
				"video_frame_rate_limit":  30,
				"video_matrix_limit":      2_000_000,
			},
		},
	}
	if version == "2.1" {
		description["version"] = "2.1"
		description["metadata"] = gin.H{}
	} else {
		description["configuration"].(gin.H)["urls"] = gin.H{}
	}
	c.JSON(http.StatusOK, description)
}
