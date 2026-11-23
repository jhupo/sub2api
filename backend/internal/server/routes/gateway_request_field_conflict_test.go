package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type conflictingFieldAPIKeyRepo struct {
	keyBillingRouteAPIKeyRepo
}

func (*conflictingFieldAPIKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error {
	return nil
}

func TestGatewayRoutesRejectRepeatedFieldsBeforeDispatch(t *testing.T) {
	for _, path := range []string{
		"/v1/responses", "/responses", "/responses/compact", "/backend-api/codex/responses",
		"/v1/chat/completions", "/chat/completions", "/v1/embeddings", "/embeddings",
		"/v1/messages", "/messages/count_tokens", "/v1/images/generations", "/videos",
		"/v1beta/models/test:generateContent", "/antigravity/v1/messages", "/antigravity/v1beta/models/test:generateContent",
	} {
		t.Run(path, func(t *testing.T) {
			router, key := newConflictingFieldRoutesTestRouter()
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"one","model":"two"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("x-api-key", key)
			request.Header.Set("x-goog-api-key", key)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), "more than once")
		})
	}
}

func newConflictingFieldRoutesTestRouter() (*gin.Engine, string) {
	gin.SetMode(gin.TestMode)
	groupID := int64(1)
	key := &service.APIKey{
		ID: 1, UserID: 1, Key: "conflict-test-key", Status: service.StatusActive,
		User:    &service.User{ID: 1, Role: service.RoleUser, Status: service.StatusActive, Balance: 10},
		GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformComposite, Status: service.StatusActive, Hydrated: true},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple, Gateway: config.GatewayConfig{MaxBodySize: 1024 * 1024, TextMaxBodySize: 1024 * 1024}}
	keyService := service.NewAPIKeyService(&conflictingFieldAPIKeyRepo{keyBillingRouteAPIKeyRepo{apiKey: key}}, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	RegisterGatewayRoutes(router, &handler.Handlers{Gateway: &handler.GatewayHandler{}, OpenAIGateway: &handler.OpenAIGatewayHandler{}, AsyncImage: handler.NewAsyncImageHandler(nil, nil)},
		servermiddleware.NewAPIKeyAuthMiddleware(keyService, nil, cfg), keyService, nil, nil, nil, nil, cfg)
	return router, key.Key
}

func TestGatewayRoutesTextLimitAppliesBeforePreread(t *testing.T) {
	for _, path := range []string{"/v1/alpha/search", "/v1/embeddings", "/backend-api/codex/alpha/search"} {
		router := newGatewayRoutesTestRouterWithConfig(&config.Config{Gateway: config.GatewayConfig{MaxBodySize: 4096, TextMaxBodySize: 32}})
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"test","input":"`+strings.Repeat("x", 128)+`"}`))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, path)
		require.Contains(t, recorder.Body.String(), "limit is 32B")
	}
}
