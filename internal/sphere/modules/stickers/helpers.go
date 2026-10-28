package stickers

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"src.solsynth.dev/sosys/lighthouse/internal/sphere/core"
)

func maxTake(c *core.Services) int {
	if c != nil && c.Config != nil && c.Config.Security.MaxTake > 0 {
		return c.Config.Security.MaxTake
	}
	return 100
}

func parseQueryInt(c *gin.Context, key string, fallback int) int {
	value := c.Query(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
