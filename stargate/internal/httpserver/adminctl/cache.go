package adminctl

// Port of Padlock's CacheAdminController (/api/admin/cache). Cache keys use
// the shared "dyson:" namespace via the Golaunch cache service, matching
// CACHE_ADMIN_API.md.

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/internal/permission"
)

// cacheStatsSnapshot mirrors Shared.Cache CacheStatsSnapshot (Redis INFO
// derived values; see CACHE_ADMIN_API.md).
type cacheStatsSnapshot struct {
	KeyspaceHits           int64   `json:"keyspace_hits"`
	KeyspaceMisses         int64   `json:"keyspace_misses"`
	TotalCommandsProcessed int64   `json:"total_commands_processed"`
	EvictedKeys            int64   `json:"evicted_keys"`
	ExpiredKeys            int64   `json:"expired_keys"`
	ConnectedClients       int64   `json:"connected_clients"`
	UsedMemoryBytes        int64   `json:"used_memory_bytes"`
	ReadCount              int64   `json:"read_count"`
	HitRatio               float64 `json:"hit_ratio"`
}

// cacheGroupResponse mirrors CacheGroupResponse.
type cacheGroupResponse struct {
	Group string   `json:"group"`
	Count int      `json:"count"`
	Keys  []string `json:"keys"`
}

// cacheClearResponse mirrors CacheClearResponse.
type cacheClearResponse struct {
	Scope        string  `json:"scope"`
	Key          *string `json:"key,omitempty"`
	Group        *string `json:"group,omitempty"`
	RemovedCount int64   `json:"removed_count"`
}

type clearCacheKeyRequest struct {
	Key string `json:"key"`
}

type clearCacheGroupRequest struct {
	Group string `json:"group"`
}

func registerCacheAdmin(g *gin.RouterGroup, d Deps) {
	g.GET("stats", requirePerm(d, permission.PermissionsCacheManage), cacheStats(d))
	g.GET("groups/:group", requirePerm(d, permission.PermissionsCacheManage), cacheGroup(d))
	g.POST("keys/clear", requirePerm(d, permission.PermissionsCacheManage), clearCacheKey(d))
	g.POST("groups/clear", requirePerm(d, permission.PermissionsCacheManage), clearCacheGroup(d))
	g.POST("clear", requirePerm(d, permission.PermissionsCacheManage), clearAllCache(d))
}

func cacheStats(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if d.Redis == nil || d.Redis.Raw == nil {
			c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "Redis is not configured.", http.StatusInternalServerError))
			return
		}
		ctx := c.Request.Context()
		stats := cacheStatsSnapshot{}
		parseInfoValue := func(info string, key string) {
			for _, line := range strings.Split(info, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, key+":") {
					if value, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, key+":")), 10, 64); err == nil {
						switch key {
						case "keyspace_hits":
							stats.KeyspaceHits = value
						case "keyspace_misses":
							stats.KeyspaceMisses = value
						case "total_commands_processed":
							stats.TotalCommandsProcessed = value
						case "evicted_keys":
							stats.EvictedKeys = value
						case "expired_keys":
							stats.ExpiredKeys = value
						case "connected_clients":
							stats.ConnectedClients = value
						case "used_memory":
							stats.UsedMemoryBytes = value
						}
					}
				}
			}
		}
		if info, err := d.Redis.Raw.Info(ctx, "stats").Result(); err == nil {
			parseInfoValue(info, "keyspace_hits")
			parseInfoValue(info, "keyspace_misses")
			parseInfoValue(info, "total_commands_processed")
			parseInfoValue(info, "evicted_keys")
			parseInfoValue(info, "expired_keys")
		}
		if info, err := d.Redis.Raw.Info(ctx, "clients").Result(); err == nil {
			parseInfoValue(info, "connected_clients")
		}
		if info, err := d.Redis.Raw.Info(ctx, "memory").Result(); err == nil {
			parseInfoValue(info, "used_memory")
		}
		stats.ReadCount = stats.KeyspaceHits + stats.KeyspaceMisses
		if stats.ReadCount > 0 {
			stats.HitRatio = float64(stats.KeyspaceHits) / float64(stats.ReadCount)
		}
		c.JSON(http.StatusOK, stats)
	}
}

func cacheGroup(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		group := c.Param("group")
		if strings.TrimSpace(group) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_CACHE_GROUP_REQUIRED", "Group is required.", http.StatusBadRequest))
			return
		}
		if d.Redis == nil || d.Redis.Cache == nil {
			c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "Cache is not configured.", http.StatusInternalServerError))
			return
		}
		keys, err := d.Redis.Cache.GetGroupKeys(c.Request.Context(), group)
		if err != nil {
			serverError(c, err, d)
			return
		}
		// The C# returns logical keys without the internal dyson: prefix.
		clean := make([]string, 0, len(keys))
		for _, key := range keys {
			clean = append(clean, strings.TrimPrefix(key, "dyson:"))
		}
		sortStrings(clean)
		c.JSON(http.StatusOK, cacheGroupResponse{Group: group, Count: len(clean), Keys: clean})
	}
}

func clearCacheKey(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request clearCacheKeyRequest
		if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Key) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_CACHE_KEY_REQUIRED", "Key is required.", http.StatusBadRequest))
			return
		}
		if d.Redis == nil || d.Redis.Cache == nil {
			c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "Cache is not configured.", http.StatusInternalServerError))
			return
		}
		key := strings.TrimSpace(request.Key)
		if err := d.Redis.Cache.Remove(c.Request.Context(), key); err != nil {
			serverError(c, err, d)
			return
		}
		if d.Log != nil {
			d.Log.Warn("admin cleared cache key", "key", key)
		}
		response := cacheClearResponse{Scope: "key", RemovedCount: 1}
		response.Key = &key
		c.JSON(http.StatusOK, response)
	}
}

func clearCacheGroup(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request clearCacheGroupRequest
		if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Group) == "" {
			c.JSON(http.StatusBadRequest, errs.New("PADLOCK_CACHE_GROUP_REQUIRED", "Group is required.", http.StatusBadRequest))
			return
		}
		if d.Redis == nil || d.Redis.Cache == nil {
			c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "Cache is not configured.", http.StatusInternalServerError))
			return
		}
		group := strings.TrimSpace(request.Group)
		keys, err := d.Redis.Cache.GetGroupKeys(c.Request.Context(), group)
		if err != nil {
			serverError(c, err, d)
			return
		}
		removed := int64(len(keys))
		if err := d.Redis.Cache.RemoveGroup(c.Request.Context(), group); err != nil {
			serverError(c, err, d)
			return
		}
		if d.Log != nil {
			d.Log.Warn("admin cleared cache group", "group", group, "keys", removed)
		}
		response := cacheClearResponse{Scope: "group", RemovedCount: removed}
		response.Group = &group
		c.JSON(http.StatusOK, response)
	}
}

func clearAllCache(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if d.Redis == nil || d.Redis.Raw == nil {
			c.JSON(http.StatusInternalServerError, errs.New("SERVER_ERROR", "Redis is not configured.", http.StatusInternalServerError))
			return
		}
		removed, err := deleteDysonKeys(c.Request.Context(), d.Redis.Raw)
		if err != nil {
			serverError(c, err, d)
			return
		}
		if d.Log != nil {
			d.Log.Warn("admin cleared all dyson cache entries", "removed", removed)
		}
		c.JSON(http.StatusOK, cacheClearResponse{Scope: "all", RemovedCount: removed})
	}
}

// deleteDysonKeys SCANs the dyson:* namespace and deletes every key, matching
// the C# cache.ClearAllAsync (the shared cache never touches keys outside
// the dyson: namespace).
func deleteDysonKeys(ctx context.Context, raw *redis.Client) (int64, error) {
	var removed int64
	var cursor uint64
	for {
		keys, next, err := raw.Scan(ctx, cursor, "dyson:*", 500).Result()
		if err != nil {
			return removed, err
		}
		if len(keys) > 0 {
			if n, err := raw.Del(ctx, keys...).Result(); err == nil {
				removed += n
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return removed, nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
