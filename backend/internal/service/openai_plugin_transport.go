package service

import "net/http"

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	if codexAdaptiveAccountEligible(account) && request.Method == http.MethodPost {
		if err := consumeOpenAIRequestAttempt(request.Context()); err != nil {
			return nil, err
		}
	}
	var response *http.Response
	var err error
	if s.pluginManager != nil {
		var handled bool
		response, handled, err = s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			s.observeOpenAIResponseQuota(request, account, response)
			return response, err
		}
	}
	response, err = s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	s.observeOpenAIResponseQuota(request, account, response)
	return response, err
}

// Observe headers when they arrive, including rejected attempts. Re-reading
// them after a long stream would give an old observation a new timestamp.
func (s *OpenAIGatewayService) observeOpenAIResponseQuota(request *http.Request, account *Account, response *http.Response) {
	if response == nil || account == nil || !account.UsesOpenAICodexProtocol() {
		return
	}
	if account.IsShadow() {
		if account.ParentAccountID != nil {
			notifyOpenAIAutoReset(*account.ParentAccountID)
		}
		return
	}
	s.UpdateCodexUsageSnapshotFromHeaders(request.Context(), account.ID, response.Header)
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
