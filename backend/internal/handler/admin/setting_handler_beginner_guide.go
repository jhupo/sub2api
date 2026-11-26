package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetBeginnerGuide(c *gin.Context) {
	doc, err := h.settingService.GetBeginnerGuide(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, doc)
}

func (h *SettingHandler) SaveBeginnerGuideDraft(c *gin.Context) {
	h.updateBeginnerGuide(c, false)
}

func (h *SettingHandler) PublishBeginnerGuide(c *gin.Context) {
	h.updateBeginnerGuide(c, true)
}

func (h *SettingHandler) updateBeginnerGuide(c *gin.Context, publish bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*1024*1024)
	var input struct {
		Revision *int64                       `json:"revision" binding:"required,gte=0"`
		Content  service.BeginnerGuideContent `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	doc, err := h.settingService.UpdateBeginnerGuide(c.Request.Context(), *input.Revision, input.Content, publish)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, doc)
}
