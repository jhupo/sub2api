package admin

import (
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type upstreamStateSettingsResponse struct {
	RotationLeadMinutes      int                         `json:"rotation_lead_minutes"`
	RetryIntervalMinutes     int                         `json:"retry_interval_minutes"`
	Enabled                  bool                        `json:"enabled"`
	AutoReplaceEnabled       bool                        `json:"auto_replace_enabled"`
	TTLMinutes               int                         `json:"ttl_minutes"`
	ExpectedLength           int                         `json:"expected_length"`
	WebshareEnabled          bool                        `json:"webshare_enabled"`
	WebshareAPIKeyConfigured bool                        `json:"webshare_api_key_configured"`
	WebshareCountryMode      string                      `json:"webshare_country_mode"`
	WebshareCountries        []string                    `json:"webshare_countries"`
	Revision                 string                      `json:"revision"`
	StateRevision            string                      `json:"state_revision"`
	Pairs                    []service.UpstreamStatePair `json:"pairs"`
}

func publicUpstreamStateSettings(v service.UpstreamStateSettings) upstreamStateSettingsResponse {
	webshareCountries := append([]string{}, v.WebshareCountries...)
	pairs := append([]service.UpstreamStatePair{}, v.Pairs...)
	return upstreamStateSettingsResponse{
		RotationLeadMinutes: v.RotationLeadMinutes, RetryIntervalMinutes: v.RetryIntervalMinutes,
		Enabled: v.Enabled, AutoReplaceEnabled: v.AutoReplaceEnabled, TTLMinutes: v.TTLMinutes, ExpectedLength: v.ExpectedLength,
		WebshareEnabled: v.WebshareEnabled, WebshareAPIKeyConfigured: v.WebshareAPIKey != "",
		WebshareCountryMode: v.WebshareCountryMode, WebshareCountries: webshareCountries,
		Revision: v.Revision, StateRevision: v.StateRevision, Pairs: pairs,
	}
}

func (h *SettingHandler) GetUpstreamStateSettings(c *gin.Context) {
	v, err := h.settingService.GetUpstreamStateSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, publicUpstreamStateSettings(v))
}

func (h *SettingHandler) SetUpstreamState(c *gin.Context) {
	if h.upstreamStateManager == nil {
		response.Error(c, 503, "State management is unavailable")
		return
	}
	var v struct {
		AccountID int64  `json:"account_id"`
		Model     string `json:"model"`
		State     string `json:"state"`
	}
	if err := c.ShouldBindJSON(&v); err != nil || v.AccountID <= 0 || strings.TrimSpace(v.Model) == "" || strings.TrimSpace(v.State) == "" {
		response.BadRequest(c, "Invalid state")
		return
	}
	result, err := h.upstreamStateManager.SetManagedUpstreamState(c.Request.Context(), v.AccountID, v.Model, v.State)
	if err != nil {
		if errors.Is(err, service.ErrUpstreamStateRefreshBusy) {
			response.Error(c, 409, err.Error())
			return
		}
		if errors.Is(err, service.ErrUpstreamStateReplaceRejected) {
			response.Error(c, 409, err.Error())
			return
		}
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, result)
}

func (h *SettingHandler) RefreshUpstreamState(c *gin.Context) {
	if h.upstreamStateManager == nil {
		response.Error(c, 503, "State management is unavailable")
		return
	}
	var v struct {
		AccountID int64  `json:"account_id"`
		Model     string `json:"model"`
	}
	if err := c.ShouldBindJSON(&v); err != nil || v.AccountID <= 0 || strings.TrimSpace(v.Model) == "" {
		response.BadRequest(c, "Invalid account/model pair")
		return
	}
	result, err := h.upstreamStateManager.RefreshManagedUpstreamState(c.Request.Context(), v.AccountID, v.Model)
	if err != nil {
		if errors.Is(err, service.ErrUpstreamStateRefreshBusy) {
			response.Error(c, 409, err.Error())
			return
		}
		if errors.Is(err, service.ErrUpstreamStateReplaceRejected) {
			response.Error(c, 409, err.Error())
			return
		}
		if errors.Is(err, service.ErrUpstreamStateRefreshFailed) {
			response.Error(c, 502, err.Error())
			return
		}
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, result)
}

func (h *SettingHandler) SetUpstreamStateSettings(c *gin.Context) {
	var v service.UpstreamStateSettings
	if err := c.ShouldBindJSON(&v); err != nil {
		response.BadRequest(c, "Invalid settings")
		return
	}
	current, err := h.settingService.GetUpstreamStateSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	v.WebshareAPIKey = strings.TrimSpace(v.WebshareAPIKey)
	if v.WebshareAPIKey == "" {
		v.WebshareAPIKey = current.WebshareAPIKey
	}
	v.WebshareCountryMode = strings.ToLower(strings.TrimSpace(v.WebshareCountryMode))
	for i := range v.WebshareCountries {
		v.WebshareCountries[i] = strings.ToUpper(strings.TrimSpace(v.WebshareCountries[i]))
	}
	if err := v.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	saved, err := h.settingService.SetUpstreamStateSettings(c.Request.Context(), v)
	if err != nil {
		if errors.Is(err, service.ErrUpstreamStateConflict) {
			response.Error(c, 409, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, publicUpstreamStateSettings(saved))
}

func (h *SettingHandler) UpstreamStateMatrix(c *gin.Context) {
	rows, err := h.settingService.UpstreamStateMatrix(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, rows)
}

func (h *SettingHandler) SetUpstreamStatePair(c *gin.Context) {
	var v struct {
		service.UpstreamStatePair
		Enabled  bool   `json:"enabled"`
		Revision string `json:"revision"`
	}
	if err := c.ShouldBindJSON(&v); err != nil {
		response.BadRequest(c, "Invalid pair")
		return
	}
	check := service.UpstreamStateSettings{RotationLeadMinutes: 10, RetryIntervalMinutes: 5, TTLMinutes: 40, ExpectedLength: 292, WebshareCountryMode: "random", Pairs: []service.UpstreamStatePair{v.UpstreamStatePair}}
	if err := check.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	saved, err := h.settingService.SetUpstreamStatePair(c.Request.Context(), v.UpstreamStatePair, v.Enabled, v.Revision)
	if err != nil {
		if errors.Is(err, service.ErrUpstreamStateConflict) {
			response.Error(c, 409, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, publicUpstreamStateSettings(saved))
}
