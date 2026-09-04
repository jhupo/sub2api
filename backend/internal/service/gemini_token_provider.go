package service

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	geminiTokenRefreshSkew = 3 * time.Minute
	geminiTokenCacheSkew   = 5 * time.Minute
	geminiProjectIDTimeout = 20 * time.Second
)

// GeminiTokenProvider manages access_token for Gemini OAuth and Vertex service account accounts.
type GeminiTokenProvider struct {
	accountRepo        AccountRepository
	tokenCache         GeminiTokenCache
	geminiOAuthService *GeminiOAuthService
	refreshAPI         *OAuthRefreshAPI
	executor           OAuthRefreshExecutor
	refreshPolicy      ProviderRefreshPolicy
	projectIDFlight    singleflight.Group
	modelCatalog       GeminiCodeAssistModelCatalog
}

func NewGeminiTokenProvider(
	accountRepo AccountRepository,
	tokenCache GeminiTokenCache,
	geminiOAuthService *GeminiOAuthService,
) *GeminiTokenProvider {
	return &GeminiTokenProvider{
		accountRepo:        accountRepo,
		tokenCache:         tokenCache,
		geminiOAuthService: geminiOAuthService,
		refreshPolicy:      GeminiProviderRefreshPolicy(),
		modelCatalog:       NewGeminiCodeAssistModelResolver(),
	}
}

// CodeAssistModelCatalog returns the process-wide resolver shared by model
// listing, admin synchronization, tests, and request forwarding for this token
// provider. The resolver cache is account-scoped internally.
func (p *GeminiTokenProvider) CodeAssistModelCatalog() GeminiCodeAssistModelCatalog {
	if p == nil {
		return nil
	}
	return p.modelCatalog
}

// SetRefreshAPI injects unified OAuth refresh API and executor.
func (p *GeminiTokenProvider) SetRefreshAPI(api *OAuthRefreshAPI, executor OAuthRefreshExecutor) {
	p.refreshAPI = api
	p.executor = executor
}

// SetRefreshPolicy injects caller-side refresh policy.
func (p *GeminiTokenProvider) SetRefreshPolicy(policy ProviderRefreshPolicy) {
	p.refreshPolicy = policy
}

func (p *GeminiTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformGemini || (account.Type != AccountTypeOAuth && account.Type != AccountTypeServiceAccount) {
		return "", errors.New("not a gemini oauth or service account")
	}
	if account.Type == AccountTypeServiceAccount {
		return p.getServiceAccountAccessToken(ctx, account)
	}
	if !account.HasSupportedGeminiOAuthType() {
		return "", errors.New("Gemini OAuth account must be re-authorized with a supported OAuth type")
	}

	cacheKey := GeminiTokenCacheKey(account)

	// 1) Try cache first.
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			p.ensureCodeAssistProjectID(ctx, account, token)
			return token, nil
		}
	}

	// 2) Refresh if needed (pre-expiry skew).
	expiresAt := account.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= geminiTokenRefreshSkew

	if needsRefresh && p.refreshAPI != nil && p.executor != nil {
		result, err := p.refreshAPI.RefreshIfNeeded(ctx, account, p.executor, geminiTokenRefreshSkew)
		if err != nil {
			if p.refreshPolicy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
		} else if result.LockHeld {
			if p.refreshPolicy.OnLockHeld == ProviderLockHeldWaitForCache && p.tokenCache != nil {
				if token, cacheErr := p.tokenCache.GetAccessToken(ctx, cacheKey); cacheErr == nil && strings.TrimSpace(token) != "" {
					return token, nil
				}
			}
			slog.Debug("gemini_token_lock_held_use_old", "account_id", account.ID)
		} else {
			account = result.Account
			expiresAt = account.GetCredentialAsTime("expires_at")
		}
	} else if needsRefresh && p.tokenCache != nil {
		// The local cache lock prevents concurrent refresh work in deployments
		// where the unified refresh coordinator is not configured.
		locked, lockErr := p.tokenCache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if lockErr == nil && locked {
			defer func() { _ = p.tokenCache.ReleaseRefreshLock(ctx, cacheKey) }()
		} else if lockErr != nil {
			slog.Warn("gemini_token_lock_failed", "account_id", account.ID, "error", lockErr)
		}
	}

	accessToken := account.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	p.ensureCodeAssistProjectID(ctx, account, accessToken)
	cacheKey = GeminiTokenCacheKey(account)

	// 3) Populate cache with TTL.
	if p.tokenCache != nil {
		latestAccount, isStale := CheckTokenVersion(ctx, account, p.accountRepo)
		if isStale && latestAccount != nil {
			slog.Debug("gemini_token_version_stale_use_latest", "account_id", account.ID)
			accessToken = latestAccount.GetCredential("access_token")
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			ttl := 30 * time.Minute
			if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > geminiTokenCacheSkew:
					ttl = until - geminiTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			_ = p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}

	return accessToken, nil
}

type geminiProjectIDDetection struct {
	projectID string
	tierID    string
}

func (p *GeminiTokenProvider) ensureCodeAssistProjectID(ctx context.Context, account *Account, accessToken string) {
	if p == nil || account == nil || strings.TrimSpace(account.GetCredential("project_id")) != "" {
		return
	}
	// Code Assist and Antigravity OAuth accounts require a Cloud Code project for
	// model discovery and generation. AI Studio accounts never enter this path.
	oauthType := account.GeminiOAuthType()
	if account.GetCredential("auto_detect_project_id") == "false" ||
		!account.IsGeminiCloudCodeOAuth() ||
		p.geminiOAuthService == nil {
		return
	}

	key := strconv.FormatInt(account.ID, 10)
	resultCh := p.projectIDFlight.DoChan(key, func() (any, error) {
		detectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), geminiProjectIDTimeout)
		defer cancel()

		var proxyURL string
		if account.ProxyID != nil && p.geminiOAuthService.proxyRepo != nil {
			if proxy, err := p.geminiOAuthService.proxyRepo.GetByID(detectCtx, *account.ProxyID); err == nil && proxy != nil {
				proxyURL = proxy.URL()
			}
		}
		projectID, tierID, err := p.geminiOAuthService.fetchProjectID(detectCtx, accessToken, proxyURL)
		if err != nil {
			return nil, err
		}
		return geminiProjectIDDetection{
			projectID: strings.TrimSpace(projectID),
			tierID:    strings.TrimSpace(tierID),
		}, nil
	})

	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		if result.Err != nil {
			log.Printf("[GeminiTokenProvider] Auto-detect project_id failed: %v", result.Err)
			return
		}
		detected, _ := result.Val.(geminiProjectIDDetection)
		if detected.projectID == "" {
			return
		}
		// Never mutate the map that was captured by the scheduler snapshot in
		// place. Several requests can observe the same account while the first
		// project discovery completes; in-place writes would race on the shared
		// map. Publishing a fresh map also keeps readers of the old snapshot
		// consistent until the durable update is visible.
		credentials := shallowCopyMap(account.Credentials)
		credentials["project_id"] = detected.projectID
		if detected.tierID != "" {
			tierID := detected.tierID
			if canonical := canonicalGeminiTierIDForOAuthType(oauthType, tierID); canonical != "" {
				tierID = canonical
			}
			credentials["tier_id"] = tierID
		}
		account.Credentials = credentials
		_ = persistAccountCredentials(ctx, p.accountRepo, account, credentials)
	}
}

func (p *GeminiTokenProvider) getServiceAccountAccessToken(ctx context.Context, account *Account) (string, error) {
	return getVertexServiceAccountAccessToken(ctx, p.tokenCache, account)
}

func GeminiTokenCacheKey(account *Account) string {
	if account != nil && account.Type == AccountTypeServiceAccount {
		if key, err := parseVertexServiceAccountKey(account); err == nil {
			return vertexServiceAccountCacheKey(account, key)
		}
	}
	projectID := strings.TrimSpace(account.GetCredential("project_id"))
	if projectID != "" {
		return "gemini:" + projectID
	}
	return "gemini:account:" + strconv.FormatInt(account.ID, 10)
}
