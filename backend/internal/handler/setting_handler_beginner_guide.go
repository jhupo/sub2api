package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetPublishedBeginnerGuide(c *gin.Context) {
	doc, err := h.settingService.GetBeginnerGuide(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, service.PublishedBeginnerGuide{Content: doc.Published, PublishedAt: doc.PublishedAt})
}
