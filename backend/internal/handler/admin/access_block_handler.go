package admin

import (
	"strconv"
	"strings"
	"time"

	accessmiddleware "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	accessBlockDefaultPageSize = 20
	accessBlockMaxPageSize     = 100
)

// AccessBlockHandler manages the policy and active IP blocks shown on the
// centralized block-management page.
type AccessBlockHandler struct {
	settingService *service.SettingService
	blocks         *accessmiddleware.AccessBlockManager
}

func NewAccessBlockHandler(settingService *service.SettingService, blocks *accessmiddleware.AccessBlockManager) *AccessBlockHandler {
	return &AccessBlockHandler{settingService: settingService, blocks: blocks}
}

type accessBlockItemDTO struct {
	IP               string `json:"ip"`
	RemainingSeconds int64  `json:"remaining_seconds"`
	Permanent        bool   `json:"permanent"`
	Source           string `json:"source"`
}

type accessBlockSettingsDTO struct {
	Enabled                     bool                              `json:"enabled"`
	LoginProtectionEnabled      bool                              `json:"login_protection_enabled"`
	LoginFailureThreshold       int                               `json:"login_failure_threshold"`
	LoginFailureWindowSeconds   int                               `json:"login_failure_window_seconds"`
	LoginTemporaryBlockSeconds  int                               `json:"login_temporary_block_seconds"`
	BlockedHeaders              []service.AccessBlockedHeaderRule `json:"blocked_headers"`
	PanelBlacklistEnabled       bool                              `json:"panel_blacklist_enabled"`
	PanelBlacklistThreshold     int                               `json:"panel_blacklist_threshold"`
	PanelBlacklistWindowSeconds int                               `json:"panel_blacklist_window_seconds"`
}

func accessBlockSettingsDTOFromSettings(settings *service.AccessBlockSettings) accessBlockSettingsDTO {
	blockedHeaders := settings.BlockedHeaders
	if blockedHeaders == nil {
		blockedHeaders = []service.AccessBlockedHeaderRule{}
	}
	return accessBlockSettingsDTO{
		Enabled:                     settings.Enabled,
		LoginProtectionEnabled:      settings.LoginProtectionEnabled,
		LoginFailureThreshold:       settings.LoginFailureThreshold,
		LoginFailureWindowSeconds:   settings.LoginFailureWindowSeconds,
		LoginTemporaryBlockSeconds:  settings.LoginTemporaryBlockSeconds,
		BlockedHeaders:              blockedHeaders,
		PanelBlacklistEnabled:       settings.PanelBlacklistEnabled,
		PanelBlacklistThreshold:     settings.PanelBlacklistThreshold,
		PanelBlacklistWindowSeconds: settings.PanelBlacklistWindowSeconds,
	}
}

func (h *AccessBlockHandler) GetSettings(c *gin.Context) {
	settings, err := h.settingService.GetAccessBlockSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, accessBlockSettingsDTOFromSettings(settings))
}

type updateAccessBlockSettingsRequest struct {
	Enabled                     *bool                              `json:"enabled"`
	LoginProtectionEnabled      *bool                              `json:"login_protection_enabled"`
	LoginFailureThreshold       *int                               `json:"login_failure_threshold"`
	LoginFailureWindowSeconds   *int                               `json:"login_failure_window_seconds"`
	LoginTemporaryBlockSeconds  *int                               `json:"login_temporary_block_seconds"`
	BlockedHeaders              *[]service.AccessBlockedHeaderRule `json:"blocked_headers"`
	PanelBlacklistEnabled       *bool                              `json:"panel_blacklist_enabled"`
	PanelBlacklistThreshold     *int                               `json:"panel_blacklist_threshold"`
	PanelBlacklistWindowSeconds *int                               `json:"panel_blacklist_window_seconds"`
}

func (h *AccessBlockHandler) UpdateSettings(c *gin.Context) {
	var req updateAccessBlockSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	settings, err := h.settingService.GetAccessBlockSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if req.Enabled != nil {
		settings.Enabled = *req.Enabled
	}
	if req.LoginProtectionEnabled != nil {
		settings.LoginProtectionEnabled = *req.LoginProtectionEnabled
	}
	if req.LoginFailureThreshold != nil {
		settings.LoginFailureThreshold = *req.LoginFailureThreshold
	}
	if req.LoginFailureWindowSeconds != nil {
		settings.LoginFailureWindowSeconds = *req.LoginFailureWindowSeconds
	}
	if req.LoginTemporaryBlockSeconds != nil {
		settings.LoginTemporaryBlockSeconds = *req.LoginTemporaryBlockSeconds
	}
	if req.BlockedHeaders != nil {
		settings.BlockedHeaders = *req.BlockedHeaders
	}
	if req.PanelBlacklistEnabled != nil {
		settings.PanelBlacklistEnabled = *req.PanelBlacklistEnabled
	}
	if req.PanelBlacklistThreshold != nil {
		settings.PanelBlacklistThreshold = *req.PanelBlacklistThreshold
	}
	if req.PanelBlacklistWindowSeconds != nil {
		settings.PanelBlacklistWindowSeconds = *req.PanelBlacklistWindowSeconds
	}
	if err := h.settingService.SetAccessBlockSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, accessBlockSettingsDTOFromSettings(settings))
}

func (h *AccessBlockHandler) List(c *gin.Context) {
	page, pageSize, ok := parseAccessBlockPage(c)
	if !ok {
		return
	}
	blocks, err := h.blocks.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	total := len(blocks)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items := make([]accessBlockItemDTO, 0, end-start)
	for _, block := range blocks[start:end] {
		items = append(items, accessBlockItemDTO{
			IP:               block.IP,
			RemainingSeconds: block.RemainingSeconds,
			Permanent:        block.Permanent,
			Source:           block.Source,
		})
	}
	response.Success(c, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func parseAccessBlockPage(c *gin.Context) (int, int, bool) {
	page := 1
	pageSize := accessBlockDefaultPageSize
	var err error
	if raw := strings.TrimSpace(c.Query("page")); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 {
			response.BadRequest(c, "Invalid page")
			return 0, 0, false
		}
	}
	if raw := strings.TrimSpace(c.Query("page_size")); raw != "" {
		pageSize, err = strconv.Atoi(raw)
		if err != nil || pageSize < 1 || pageSize > accessBlockMaxPageSize {
			response.BadRequest(c, "Invalid page_size")
			return 0, 0, false
		}
	}
	return page, pageSize, true
}

type addAccessBlockRequest struct {
	IP              string `json:"ip" binding:"required"`
	Permanent       bool   `json:"permanent"`
	DurationSeconds int    `json:"duration_seconds"`
}

func (h *AccessBlockHandler) Add(c *gin.Context) {
	var req addAccessBlockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.blocks.Add(
		c.Request.Context(),
		req.IP,
		req.Permanent,
		time.Duration(req.DurationSeconds)*time.Second,
	); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"ip": strings.TrimSpace(req.IP), "permanent": req.Permanent})
}

type removeAccessBlockRequest struct {
	IP string `json:"ip" binding:"required"`
}

func (h *AccessBlockHandler) Remove(c *gin.Context) {
	var req removeAccessBlockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.blocks.Remove(c.Request.Context(), req.IP); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"ip": strings.TrimSpace(req.IP)})
}
