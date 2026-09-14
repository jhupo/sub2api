package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func requestVisibleModelForTest(h *GatewayHandler, group *service.Group, modelID string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if modelID != "" {
		c.Params = gin.Params{{Key: "model", Value: modelID}}
	}
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{GroupID: &group.ID, Group: group})
	h.Models(c)
	return recorder
}

func TestRetrieveModelMatchesVisibleCatalogue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{ID: 71, Platform: service.PlatformOpenAI}
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{
		group.ID: {{
			ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"custom-model": "upstream-model"}},
		}},
	}})

	list := requestVisibleModelForTest(h, group, "")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var catalog struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &catalog))
	require.Len(t, catalog.Data, 1)
	require.Contains(t, string(catalog.Data[0]), `"id":"custom-model"`)

	retrieved := requestVisibleModelForTest(h, group, "custom-model")
	require.Equal(t, http.StatusOK, retrieved.Code, retrieved.Body.String())
	require.JSONEq(t, string(catalog.Data[0]), retrieved.Body.String())

	missing := requestVisibleModelForTest(h, group, "upstream-model")
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Contains(t, missing.Body.String(), `"code":"model_not_found"`)
}

func TestWriteRetrievedModelPreservesExactEntry(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{{Key: "model", Value: "special-model"}}
	writeRetrievedModel(c, []byte(`{"object":"list","data":[{"id":"special-model","owned_by":"source-owner","created":123,"extra":{"context":999}}]}`))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"id":"special-model","owned_by":"source-owner","created":123,"extra":{"context":999}}`, recorder.Body.String())
}

func TestWriteRetrievedModelRejectsInvalidCatalogue(t *testing.T) {
	for name, body := range map[string]string{
		"catalogue": `{`,
		"entry":     `{"data":[null]}`,
		"id":        `{"data":[{"id":7}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Params = gin.Params{{Key: "model", Value: "model"}}
			writeRetrievedModel(c, []byte(body))
			require.Equal(t, http.StatusBadGateway, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"type":"upstream_error"`)
		})
	}
}
