package admin

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type cacheAdjustmentAccountReader interface {
	GetByIDs(context.Context, []int64) ([]*service.Account, error)
}

func (h *SettingHandler) SetCacheReadAdjustmentAccounts(accounts service.AccountRepository) {
	h.cacheAdjustmentAccounts = accounts
}

func (h *SettingHandler) ListCacheReadAdjustmentAccounts(c *gin.Context) {
	settings, err := h.settingService.GetCacheReadAdjustmentSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	page = max(1, page)
	pageSize = min(100, max(1, pageSize))
	ids := slices.Clone(settings.AccountIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	total := len(ids)
	pages := max(1, (total+pageSize-1)/pageSize)
	page = min(page, pages)
	start := (page - 1) * pageSize
	ids = ids[start:min(total, start+pageSize)]
	accounts, err := h.cacheAdjustmentAccounts.GetByIDs(c.Request.Context(), ids)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	byID := make(map[int64]*service.Account, len(accounts))
	for _, account := range accounts {
		if account != nil {
			byID[account.ID] = account
		}
	}
	type summary struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Platform string `json:"platform"`
		Missing  bool   `json:"missing"`
	}
	items := make([]summary, 0, len(ids))
	for _, id := range ids {
		item := summary{ID: id, Missing: true}
		if account := byID[id]; account != nil {
			item.Name, item.Platform, item.Missing = account.Name, account.Platform, false
		}
		items = append(items, item)
	}
	response.Success(c, response.PaginatedData{Items: items, Total: int64(total), Page: page, PageSize: pageSize, Pages: pages})
}

func (h *SettingHandler) GetCacheReadAdjustmentSettings(c *gin.Context) {
	settings, err := h.settingService.GetCacheReadAdjustmentSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *SettingHandler) UpdateCacheReadAdjustmentSettings(c *gin.Context) {
	var settings service.CacheReadAdjustmentSettings
	if err := c.ShouldBindJSON(&settings); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := settings.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.settingService.SetCacheReadAdjustmentSettings(c.Request.Context(), &settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	slices.Sort(settings.AccountIDs)
	settings.AccountIDs = slices.Compact(settings.AccountIDs)
	response.Success(c, settings)
}

func (h *SettingHandler) PatchCacheReadAdjustmentSettings(c *gin.Context) {
	var patch service.CacheReadAdjustmentPatch
	if err := c.ShouldBindJSON(&patch); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	h.patchCacheReadAdjustment(c, &patch)
}

func (h *SettingHandler) AddCacheReadAdjustmentAccounts(c *gin.Context) {
	h.changeCacheReadAdjustmentAccounts(c, true)
}

func (h *SettingHandler) RemoveCacheReadAdjustmentAccounts(c *gin.Context) {
	h.changeCacheReadAdjustmentAccounts(c, false)
}

func (h *SettingHandler) changeCacheReadAdjustmentAccounts(c *gin.Context, add bool) {
	var body struct {
		AccountIDs []int64 `json:"account_ids" binding:"required,min=1,max=1000"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	patch := &service.CacheReadAdjustmentPatch{RemoveAccountIDs: body.AccountIDs}
	if add {
		patch = &service.CacheReadAdjustmentPatch{AddAccountIDs: body.AccountIDs}
	}
	h.patchCacheReadAdjustment(c, patch)
}

func (h *SettingHandler) patchCacheReadAdjustment(c *gin.Context, patch *service.CacheReadAdjustmentPatch) {
	if err := patch.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if len(patch.AddAccountIDs) > 0 {
		ids := slices.Clone(patch.AddAccountIDs)
		slices.Sort(ids)
		ids = slices.Compact(ids)
		accounts, err := h.cacheAdjustmentAccounts.GetByIDs(c.Request.Context(), ids)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		found := make(map[int64]bool, len(accounts))
		for _, account := range accounts {
			if account != nil {
				found[account.ID] = true
			}
		}
		for _, id := range ids {
			if !found[id] {
				response.BadRequest(c, fmt.Sprintf("account %d not found", id))
				return
			}
		}
	}
	settings, err := h.settingService.PatchCacheReadAdjustmentSettings(c.Request.Context(), patch)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
