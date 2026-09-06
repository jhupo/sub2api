package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

type AccessBlockedHeaderRule struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AccessBlockSettings is the policy owned by the access-block feature. Panel
// rate-limit quotas remain in PanelRateLimitSettings and are not duplicated.
type AccessBlockSettings struct {
	Enabled                     bool                      `json:"enabled"`
	LoginProtectionEnabled      bool                      `json:"login_protection_enabled"`
	LoginFailureThreshold       int                       `json:"login_failure_threshold"`
	LoginFailureWindowSeconds   int                       `json:"login_failure_window_seconds"`
	LoginTemporaryBlockSeconds  int                       `json:"login_temporary_block_seconds"`
	BlockedHeaders              []AccessBlockedHeaderRule `json:"blocked_headers"`
	PanelBlacklistEnabled       bool                      `json:"panel_blacklist_enabled"`
	PanelBlacklistThreshold     int                       `json:"panel_blacklist_threshold"`
	PanelBlacklistWindowSeconds int                       `json:"panel_blacklist_window_seconds"`
}

const (
	loginFailureThresholdDefault      = 10
	loginFailureWindowSecondsDefault  = 10 * 60
	loginTemporaryBlockSecondsDefault = 60 * 60
	loginFailureThresholdMax          = 1000
	loginFailureWindowSecondsMax      = 24 * 60 * 60
	loginTemporaryBlockSecondsMax     = 7 * 24 * 60 * 60
	panelBlacklistThresholdDefault    = 10
	panelBlacklistWindowDefault       = 10 * 60
	panelBlacklistThresholdMax        = 1000
	panelBlacklistWindowMax           = 24 * 60 * 60
	accessBlockSettingsCacheTTL       = 60 * time.Second
	accessBlockSettingsErrorTTL       = 5 * time.Second
	accessBlockSettingsDBTimeout      = 5 * time.Second
)

type cachedAccessBlockSettings struct {
	settings  AccessBlockSettings
	expiresAt int64
}

func DefaultAccessBlockSettings() *AccessBlockSettings {
	return &AccessBlockSettings{
		Enabled:                     true,
		LoginProtectionEnabled:      true,
		LoginFailureThreshold:       loginFailureThresholdDefault,
		LoginFailureWindowSeconds:   loginFailureWindowSecondsDefault,
		LoginTemporaryBlockSeconds:  loginTemporaryBlockSecondsDefault,
		BlockedHeaders:              []AccessBlockedHeaderRule{},
		PanelBlacklistEnabled:       false,
		PanelBlacklistThreshold:     panelBlacklistThresholdDefault,
		PanelBlacklistWindowSeconds: panelBlacklistWindowDefault,
	}
}

func normalizeAccessBlockSettings(settings *AccessBlockSettings) {
	if settings == nil {
		return
	}
	settings.LoginFailureThreshold = min(max(settings.LoginFailureThreshold, 0), loginFailureThresholdMax)
	settings.LoginFailureWindowSeconds = min(max(settings.LoginFailureWindowSeconds, 0), loginFailureWindowSecondsMax)
	settings.LoginTemporaryBlockSeconds = min(max(settings.LoginTemporaryBlockSeconds, 0), loginTemporaryBlockSecondsMax)
	settings.PanelBlacklistThreshold = min(max(settings.PanelBlacklistThreshold, 0), panelBlacklistThresholdMax)
	settings.PanelBlacklistWindowSeconds = min(max(settings.PanelBlacklistWindowSeconds, 0), panelBlacklistWindowMax)
	if len(settings.BlockedHeaders) > 20 {
		settings.BlockedHeaders = settings.BlockedHeaders[:20]
	}
	cleaned := make([]AccessBlockedHeaderRule, 0, len(settings.BlockedHeaders))
	seen := make(map[string]struct{}, len(settings.BlockedHeaders))
	for _, rule := range settings.BlockedHeaders {
		name := http.CanonicalHeaderKey(strings.TrimSpace(rule.Name))
		value := strings.TrimSpace(rule.Value)
		if !httpguts.ValidHeaderFieldName(name) || value == "" || len(value) > 256 || !httpguts.ValidHeaderFieldValue(value) {
			continue
		}
		key := strings.ToLower(name) + "\x00" + value
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, AccessBlockedHeaderRule{Name: name, Value: value})
	}
	settings.BlockedHeaders = cleaned
}

func validateAccessBlockSettings(settings *AccessBlockSettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	if settings.LoginProtectionEnabled {
		if settings.LoginFailureThreshold < 1 || settings.LoginFailureThreshold > loginFailureThresholdMax {
			return fmt.Errorf("login failure threshold must be between 1 and %d", loginFailureThresholdMax)
		}
		if settings.LoginFailureWindowSeconds < 10 || settings.LoginFailureWindowSeconds > loginFailureWindowSecondsMax {
			return fmt.Errorf("login failure window must be between 10 and %d seconds", loginFailureWindowSecondsMax)
		}
		if settings.LoginTemporaryBlockSeconds < 10 || settings.LoginTemporaryBlockSeconds > loginTemporaryBlockSecondsMax {
			return fmt.Errorf("login temporary block duration must be between 10 and %d seconds", loginTemporaryBlockSecondsMax)
		}
	}
	if len(settings.BlockedHeaders) > 20 {
		return fmt.Errorf("at most 20 blocked header rules are allowed")
	}
	for _, rule := range settings.BlockedHeaders {
		name := http.CanonicalHeaderKey(strings.TrimSpace(rule.Name))
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("invalid blocked header name")
		}
		value := strings.TrimSpace(rule.Value)
		if value == "" || len(value) > 256 || !httpguts.ValidHeaderFieldValue(value) {
			return fmt.Errorf("blocked header values must be 1-256 characters")
		}
	}
	if settings.PanelBlacklistEnabled {
		if settings.PanelBlacklistThreshold < 1 || settings.PanelBlacklistThreshold > panelBlacklistThresholdMax {
			return fmt.Errorf("panel blacklist threshold must be between 1 and %d", panelBlacklistThresholdMax)
		}
		if settings.PanelBlacklistWindowSeconds < 10 || settings.PanelBlacklistWindowSeconds > panelBlacklistWindowMax {
			return fmt.Errorf("panel blacklist window must be between 10 and %d seconds", panelBlacklistWindowMax)
		}
	}
	return nil
}

func (s *SettingService) GetAccessBlockSettings(ctx context.Context) (*AccessBlockSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAccessBlockSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultAccessBlockSettings(), nil
		}
		return nil, fmt.Errorf("get access block settings: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		return DefaultAccessBlockSettings(), nil
	}
	settings := DefaultAccessBlockSettings()
	if err := json.Unmarshal([]byte(value), settings); err != nil {
		slog.Warn("failed to unmarshal access block settings, falling back to defaults", "error", err)
		return DefaultAccessBlockSettings(), nil
	}
	normalizeAccessBlockSettings(settings)
	return settings, nil
}

func (s *SettingService) SetAccessBlockSettings(ctx context.Context, settings *AccessBlockSettings) error {
	if err := validateAccessBlockSettings(settings); err != nil {
		return err
	}
	settingsCopy := *settings
	settingsCopy.BlockedHeaders = append([]AccessBlockedHeaderRule(nil), settings.BlockedHeaders...)
	normalizeAccessBlockSettings(&settingsCopy)
	data, err := json.Marshal(&settingsCopy)
	if err != nil {
		return fmt.Errorf("marshal access block settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyAccessBlockSettings, string(data)); err != nil {
		return err
	}
	s.storeAccessBlockSettingsCache(settingsCopy, accessBlockSettingsCacheTTL)
	*settings = settingsCopy
	return nil
}

func (s *SettingService) GetAccessBlockSettingsCached(ctx context.Context) AccessBlockSettings {
	if s == nil || s.settingRepo == nil {
		return *DefaultAccessBlockSettings()
	}
	if cached, ok := s.accessBlockSettingsCache.Load().(*cachedAccessBlockSettings); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.settings
	}
	result, _, _ := s.accessBlockSettingsSF.Do("access_block_settings", func() (any, error) {
		if cached, ok := s.accessBlockSettingsCache.Load().(*cachedAccessBlockSettings); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
			return cached.settings, nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accessBlockSettingsDBTimeout)
		defer cancel()
		settings, err := s.GetAccessBlockSettings(dbCtx)
		if err != nil {
			slog.Warn("failed to get access block settings", "error", err)
			fallback := *DefaultAccessBlockSettings()
			if prior, ok := s.accessBlockSettingsCache.Load().(*cachedAccessBlockSettings); ok && prior != nil {
				fallback = prior.settings
			}
			s.storeAccessBlockSettingsCache(fallback, accessBlockSettingsErrorTTL)
			return fallback, nil
		}
		s.storeAccessBlockSettingsCache(*settings, accessBlockSettingsCacheTTL)
		return *settings, nil
	})
	if settings, ok := result.(AccessBlockSettings); ok {
		return settings
	}
	return *DefaultAccessBlockSettings()
}

func (s *SettingService) storeAccessBlockSettingsCache(settings AccessBlockSettings, ttl time.Duration) {
	settings.BlockedHeaders = append([]AccessBlockedHeaderRule(nil), settings.BlockedHeaders...)
	s.accessBlockSettingsCache.Store(&cachedAccessBlockSettings{
		settings:  settings,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}
