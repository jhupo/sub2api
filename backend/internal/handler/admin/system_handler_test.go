//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type systemHandlerUpdateServiceStub struct {
	performErr              error
	updateInfo              *service.UpdateInfo
	checkErr                error
	checkForces             []bool
	performCall             int
	performOperationIDs     []string
	performCtxErr           error
	performHasDeadline      bool
	updateExecution         *service.UpdateExecution
	needsRestart            bool
	rollbackCall            int
	rollbackToCall          int
	rollbackToCtxErr        error
	rollbackToHasDeadline   bool
	rollbackToVersions      []string
	rollbackOperationIDs    []string
	rollbackToErr           error
	rollbackExecution       *service.UpdateExecution
	rollbackVersions        []service.RollbackVersion
	rollbackVersionsErr     error
	rollbackVersionsCall    int
	updateStatus            *service.UpdateRolloutStatus
	updateStatusErr         error
	updateStatusOperations  []string
	activeRolloutErr        error
	activeRolloutOperations []string
}

func (s *systemHandlerUpdateServiceStub) CheckUpdate(_ context.Context, force bool) (*service.UpdateInfo, error) {
	s.checkForces = append(s.checkForces, force)
	return s.updateInfo, s.checkErr
}

func (s *systemHandlerUpdateServiceStub) PerformUpdate(ctx context.Context, operationID string) (*service.UpdateExecution, error) {
	s.performCall++
	s.performOperationIDs = append(s.performOperationIDs, operationID)
	s.performCtxErr = ctx.Err()
	_, s.performHasDeadline = ctx.Deadline()
	return s.updateExecution, s.performErr
}

func (s *systemHandlerUpdateServiceStub) GetUpdateStatus(_ context.Context, operationID string) (*service.UpdateRolloutStatus, error) {
	s.updateStatusOperations = append(s.updateStatusOperations, operationID)
	return s.updateStatus, s.updateStatusErr
}

func (s *systemHandlerUpdateServiceStub) EnsureNoActiveRollout(_ context.Context, operationID string) error {
	s.activeRolloutOperations = append(s.activeRolloutOperations, operationID)
	return s.activeRolloutErr
}

func (s *systemHandlerUpdateServiceStub) NeedsRestart() bool {
	return s.needsRestart
}

func (s *systemHandlerUpdateServiceStub) Rollback() error {
	s.rollbackCall++
	return nil
}

func (s *systemHandlerUpdateServiceStub) ListRollbackVersions(context.Context) ([]service.RollbackVersion, error) {
	s.rollbackVersionsCall++
	return s.rollbackVersions, s.rollbackVersionsErr
}

func (s *systemHandlerUpdateServiceStub) RollbackToVersion(ctx context.Context, version, operationID string) (*service.UpdateExecution, error) {
	s.rollbackToCall++
	s.rollbackToCtxErr = ctx.Err()
	_, s.rollbackToHasDeadline = ctx.Deadline()
	s.rollbackToVersions = append(s.rollbackToVersions, version)
	s.rollbackOperationIDs = append(s.rollbackOperationIDs, operationID)
	return s.rollbackExecution, s.rollbackToErr
}

type systemUpdateResponseEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Message         string `json:"message"`
		AlreadyUpToDate bool   `json:"already_up_to_date"`
		CurrentVersion  string `json:"current_version"`
		LatestVersion   string `json:"latest_version"`
		OperationID     string `json:"operation_id"`
		NeedRestart     bool   `json:"need_restart"`
		Pending         bool   `json:"pending"`
		TargetVersion   string `json:"target_version"`
	} `json:"data"`
}

type systemUpdateErrorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newSystemHandlerTestRouter(t *testing.T, updateSvc *systemHandlerUpdateServiceStub, repo *memoryIdempotencyRepoStub) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	service.SetDefaultIdempotencyCoordinator(nil)
	t.Cleanup(func() {
		service.SetDefaultIdempotencyCoordinator(nil)
	})

	lockSvc := service.NewSystemOperationLockService(repo, service.IdempotencyConfig{
		ProcessingTimeout:  time.Second,
		SystemOperationTTL: time.Minute,
	})
	handler := NewSystemHandler(updateSvc, lockSvc)

	router := gin.New()
	router.POST("/api/v1/admin/system/update", handler.PerformUpdate)
	router.GET("/api/v1/admin/system/update-status/:operation_id", handler.GetUpdateStatus)
	router.POST("/api/v1/admin/system/rollback", handler.Rollback)
	router.GET("/api/v1/admin/system/rollback-versions", handler.GetRollbackVersions)
	router.POST("/api/v1/admin/system/restart", handler.RestartService)
	return router
}

func requireSystemLockStatus(t *testing.T, repo *memoryIdempotencyRepoStub, wantStatus string) {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()

	for _, record := range repo.data {
		if record.Status == wantStatus {
			return
		}
	}
	t.Fatalf("system lock status %q not found in records: %#v", wantStatus, repo.data)
}

func TestSystemHandlerPerformUpdateAlreadyUpToDateReturnsOK(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{
		performErr: service.ErrNoUpdateAvailable,
		updateInfo: &service.UpdateInfo{
			CurrentVersion: "0.1.132",
			LatestVersion:  "0.1.132",
			HasUpdate:      false,
		},
	}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/update", nil)
	req.Header.Set("Idempotency-Key", "already-up-to-date")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, updateSvc.performCall)
	require.Equal(t, []bool{false}, updateSvc.checkForces)
	requireSystemLockStatus(t, repo, service.IdempotencyStatusSucceeded)

	var body systemUpdateResponseEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.Equal(t, "success", body.Message)
	require.Equal(t, "Already up to date", body.Data.Message)
	require.True(t, body.Data.AlreadyUpToDate)
	require.Equal(t, "0.1.132", body.Data.CurrentVersion)
	require.Equal(t, "0.1.132", body.Data.LatestVersion)
	require.False(t, body.Data.Pending)
	require.False(t, body.Data.NeedRestart)
	require.Equal(t, "0.1.132", body.Data.TargetVersion)
	require.NotEmpty(t, body.Data.OperationID)
}

func TestSystemHandlerPerformUpdateReportsPendingRollout(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{
		updateExecution: &service.UpdateExecution{
			TargetVersion: "0.1.242",
			Pending:       true,
		},
	}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/update", nil)
	req.Header.Set("Idempotency-Key", "pending-update")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body systemUpdateResponseEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.True(t, body.Data.Pending)
	require.False(t, body.Data.NeedRestart)
	require.Equal(t, "0.1.242", body.Data.TargetVersion)
	require.Equal(t, []string{body.Data.OperationID}, updateSvc.performOperationIDs)
	require.Contains(t, body.Data.Message, "scheduled")
}

func TestSystemHandlerGetUpdateStatus(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{updateStatus: &service.UpdateRolloutStatus{
		OperationID:    "sysop-status123",
		Status:         service.UpdateStatusSucceeded,
		CurrentVersion: "0.1.241",
		TargetVersion:  "0.1.242",
	}}
	router := newSystemHandlerTestRouter(t, updateSvc, newMemoryIdempotencyRepoStub())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/system/update-status/sysop-status123", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"sysop-status123"}, updateSvc.updateStatusOperations)
	var body struct {
		Data service.UpdateRolloutStatus `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, service.UpdateStatusSucceeded, body.Data.Status)
	require.Equal(t, "0.1.242", body.Data.TargetVersion)
}

func TestSystemHandlerGetUpdateStatusRejectsInvalidOperationID(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{updateStatusErr: service.ErrUpdateOperationIDInvalid}
	router := newSystemHandlerTestRouter(t, updateSvc, newMemoryIdempotencyRepoStub())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/system/update-status/not-a-system-operation", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSystemHandlerPerformUpdateFailureStillReturnsInternalError(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{
		performErr: errors.New("download failed"),
	}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/update", nil)
	req.Header.Set("Idempotency-Key", "real-failure")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, 1, updateSvc.performCall)
	require.Empty(t, updateSvc.checkForces)
	requireSystemLockStatus(t, repo, service.IdempotencyStatusFailedRetryable)

	var body systemUpdateErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, http.StatusInternalServerError, body.Code)
	require.Equal(t, "internal error", body.Message)
}

func TestSystemHandlerActiveRolloutBlocksSystemMutations(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		body        string
		contentType string
	}{
		{name: "update", path: "/api/v1/admin/system/update"},
		{name: "rollback", path: "/api/v1/admin/system/rollback", body: `{"version":"0.1.240"}`, contentType: "application/json"},
		{name: "restart", path: "/api/v1/admin/system/restart"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updateSvc := &systemHandlerUpdateServiceStub{activeRolloutErr: service.ErrSystemOperationBusy}
			repo := newMemoryIdempotencyRepoStub()
			router := newSystemHandlerTestRouter(t, updateSvc, repo)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			req.Header.Set("Idempotency-Key", "blocked-"+tt.name)
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusConflict, rec.Code)
			require.Len(t, updateSvc.activeRolloutOperations, 1)
			require.Equal(t, 0, updateSvc.performCall)
			require.Equal(t, 0, updateSvc.rollbackCall)
			require.Equal(t, 0, updateSvc.rollbackToCall)
			requireSystemLockStatus(t, repo, service.IdempotencyStatusFailedRetryable)
		})
	}
}

// TestSystemHandlerPerformUpdateSurvivesClientDisconnect reproduces #4504:
// the browser or a reverse proxy (axios 30s default, nginx proxy_read_timeout
// 60s) aborts the long-running update request and cancels the request
// context. The download must keep running on a detached context
// instead of dying with "download failed: context canceled".
func TestSystemHandlerPerformUpdateSurvivesClientDisconnect(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/update", nil)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	req = req.WithContext(canceledCtx)
	req.Header.Set("Idempotency-Key", "disconnected-update")
	router.ServeHTTP(rec, req)

	require.Equal(t, 1, updateSvc.performCall)
	require.NoError(t, updateSvc.performCtxErr,
		"update must not observe the canceled request context")
	require.False(t, updateSvc.performHasDeadline,
		"aggregate update deadlines can bypass orchestrator rollback")
	requireSystemLockStatus(t, repo, service.IdempotencyStatusSucceeded)
}

func TestSystemHandlerRollbackToVersionSurvivesClientDisconnect(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/rollback",
		strings.NewReader(`{"version":"0.1.146"}`))
	req.Header.Set("Content-Type", "application/json")
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	req = req.WithContext(canceledCtx)
	req.Header.Set("Idempotency-Key", "disconnected-rollback")
	router.ServeHTTP(rec, req)

	require.Equal(t, 1, updateSvc.rollbackToCall)
	require.NoError(t, updateSvc.rollbackToCtxErr,
		"versioned rollback must not observe the canceled request context")
	require.False(t, updateSvc.rollbackToHasDeadline,
		"aggregate rollback deadlines can bypass orchestrator rollback")
	requireSystemLockStatus(t, repo, service.IdempotencyStatusSucceeded)
}

func TestSystemHandlerRollbackToVersionReportsPendingRollout(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{
		rollbackExecution: &service.UpdateExecution{
			TargetVersion: "0.1.240",
			Pending:       true,
		},
	}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/rollback",
		strings.NewReader(`{"version":"0.1.240"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "pending-rollback")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body systemUpdateResponseEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.True(t, body.Data.Pending)
	require.False(t, body.Data.NeedRestart)
	require.Equal(t, "0.1.240", body.Data.TargetVersion)
	require.Contains(t, body.Data.Message, "scheduled")
}

func TestSystemHandlerRollbackWithoutBodyUsesLegacyBackup(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/rollback", nil)
	req.Header.Set("Idempotency-Key", "legacy-rollback")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, updateSvc.rollbackCall)
	require.Equal(t, 0, updateSvc.rollbackToCall)
	requireSystemLockStatus(t, repo, service.IdempotencyStatusSucceeded)
}

func TestSystemHandlerRollbackWithVersionCallsRollbackToVersion(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/rollback",
		strings.NewReader(`{"version":"0.1.146"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "rollback-to-146")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, updateSvc.rollbackCall)
	require.Equal(t, 1, updateSvc.rollbackToCall)
	require.Equal(t, []string{"0.1.146"}, updateSvc.rollbackToVersions)
	require.Len(t, updateSvc.rollbackOperationIDs, 1)
	require.True(t, strings.HasPrefix(updateSvc.rollbackOperationIDs[0], "sysop-"))
	requireSystemLockStatus(t, repo, service.IdempotencyStatusSucceeded)

	var body systemUpdateResponseEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.Equal(t, "Rollback completed. Please restart the service.", body.Data.Message)
}

func TestSystemHandlerRollbackWithDisallowedVersionReturnsBadRequest(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{
		rollbackToErr: service.ErrRollbackVersionNotAllowed,
	}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/rollback",
		strings.NewReader(`{"version":"9.9.9"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "rollback-to-bad")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, 1, updateSvc.rollbackToCall)
}

func TestSystemHandlerGetRollbackVersions(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{
		rollbackVersions: []service.RollbackVersion{
			{Version: "0.1.146", PublishedAt: "2026-07-07T00:00:00Z", HTMLURL: "https://example.com/v0.1.146"},
			{Version: "0.1.145", PublishedAt: "2026-07-06T00:00:00Z", HTMLURL: "https://example.com/v0.1.145"},
		},
	}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/system/rollback-versions", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, updateSvc.rollbackVersionsCall)

	var body struct {
		Code int `json:"code"`
		Data struct {
			Versions []service.RollbackVersion `json:"versions"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.Len(t, body.Data.Versions, 2)
	require.Equal(t, "0.1.146", body.Data.Versions[0].Version)
}

func TestSystemHandlerGetRollbackVersionsError(t *testing.T) {
	updateSvc := &systemHandlerUpdateServiceStub{
		rollbackVersionsErr: errors.New("github unavailable"),
	}
	repo := newMemoryIdempotencyRepoStub()
	router := newSystemHandlerTestRouter(t, updateSvc, repo)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/system/rollback-versions", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
