// Package system is the WebUI health boundary — the liveness endpoint.
package system

import (
	"github.com/gin-gonic/gin"
)

// Register mounts this boundary's routes under /api.
func Register(api *gin.RouterGroup) {
	api.GET("/health", health)
}

func health(c *gin.Context) {
	c.JSON(200, struct {
		Status string `json:"status"`
	}{Status: "ok"})
}
