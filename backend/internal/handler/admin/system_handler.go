package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/sysutil"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// SystemHandler handles system-related operations
type SystemHandler struct {
	updateSvc systemUpdateService
	lockSvc   *service.SystemOperationLockService
}

// systemUpdateContext detaches a long-running update/rollback from the HTTP
// request lifetime. Browsers and reverse proxies commonly abort idle requests
// after 30-60s (axios default, nginx proxy_read_timeout), which canceled
// c.Request.Context() mid-download and killed the update with
// "download failed: context canceled" (#4504). Network clients and the
// orchestrator own their per-step deadlines; an aggregate deadline is unsafe
// because rollout time scales with the number of services and forced process
// termination bypasses the orchestrator's rollback and terminal status trap.
func systemUpdateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithCancel(base)
}

type systemUpdateService interface {
	CheckUpdate(ctx context.Context, force bool) (*service.UpdateInfo, error)
	PerformUpdate(ctx context.Context, operationID string) (*service.UpdateExecution, error)
	GetUpdateStatus(ctx context.Context, operationID string) (*service.UpdateRolloutStatus, error)
	EnsureNoActiveRollout(ctx context.Context, operationID string) error
	NeedsRestart() bool
	Rollback() error
	ListRollbackVersions(ctx context.Context) ([]service.RollbackVersion, error)
	RollbackToVersion(ctx context.Context, version, operationID string) (*service.UpdateExecution, error)
}

// NewSystemHandler creates a new SystemHandler
func NewSystemHandler(updateSvc systemUpdateService, lockSvc *service.SystemOperationLockService) *SystemHandler {
	return &SystemHandler{
		updateSvc: updateSvc,
		lockSvc:   lockSvc,
	}
}

// GetVersion returns the current version
// GET /api/v1/admin/system/version
func (h *SystemHandler) GetVersion(c *gin.Context) {
	info, _ := h.updateSvc.CheckUpdate(c.Request.Context(), false)
	response.Success(c, gin.H{
		"version": info.CurrentVersion,
	})
}

// CheckUpdates checks for available updates
// GET /api/v1/admin/system/check-updates
func (h *SystemHandler) CheckUpdates(c *gin.Context) {
	force := c.Query("force") == "true"
	info, err := h.updateSvc.CheckUpdate(c.Request.Context(), force)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, info)
}

// PerformUpdate downloads and applies the update
// POST /api/v1/admin/system/update
func (h *SystemHandler) PerformUpdate(c *gin.Context) {
	operationID := buildSystemOperationID(c, "update")
	payload := gin.H{"operation_id": operationID}
	executeAdminIdempotentJSON(c, "admin.system.update", payload, service.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		lock, release, err := h.acquireSystemLock(ctx, operationID)
		if err != nil {
			return nil, err
		}
		var releaseReason string
		succeeded := false
		defer func() {
			release(releaseReason, succeeded)
		}()

		updateCtx, cancel := systemUpdateContext(ctx)
		defer cancel()

		execution, err := h.updateSvc.PerformUpdate(updateCtx, lock.OperationID())
		if err != nil {
			if errors.Is(err, service.ErrNoUpdateAvailable) {
				info, checkErr := h.updateSvc.CheckUpdate(updateCtx, false)
				if checkErr != nil {
					releaseReason = "SYSTEM_UPDATE_FAILED"
					return nil, checkErr
				}
				succeeded = true
				return gin.H{
					"message":            "Already up to date",
					"already_up_to_date": true,
					"current_version":    info.CurrentVersion,
					"latest_version":     info.LatestVersion,
					"need_restart":       false,
					"pending":            false,
					"target_version":     info.CurrentVersion,
					"operation_id":       lock.OperationID(),
				}, nil
			}
			releaseReason = "SYSTEM_UPDATE_FAILED"
			return nil, err
		}
		succeeded = true
		pending := execution != nil && execution.Pending
		needsRestart := !pending && h.updateSvc.NeedsRestart()
		message := "Update completed. Please restart the service."
		if pending {
			message = "Update rollout scheduled. Waiting for the new service to become ready."
		} else if !needsRestart {
			message = "Update completed and health checks passed."
		}
		targetVersion := ""
		if execution != nil {
			targetVersion = execution.TargetVersion
		}

		return gin.H{
			"message":        message,
			"need_restart":   needsRestart,
			"pending":        pending,
			"target_version": targetVersion,
			"operation_id":   lock.OperationID(),
		}, nil
	})
}

// GetUpdateStatus returns the authoritative state written by the detached
// rollout helper after readiness verification or rollback.
// GET /api/v1/admin/system/update-status/:operation_id
func (h *SystemHandler) GetUpdateStatus(c *gin.Context) {
	status, err := h.updateSvc.GetUpdateStatus(c.Request.Context(), c.Param("operation_id"))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, status)
}

// GetRollbackVersions lists versions available for rollback
// GET /api/v1/admin/system/rollback-versions
func (h *SystemHandler) GetRollbackVersions(c *gin.Context) {
	versions, err := h.updateSvc.ListRollbackVersions(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, gin.H{
		"versions": versions,
	})
}

// Rollback restores a previous version.
// Without a body (or with an empty version) it restores the local .backup binary
// left by the last in-place update. With {"version": "x.y.z"} it downloads and
// installs that specific release (must be one of the recent rollback versions).
// POST /api/v1/admin/system/rollback
func (h *SystemHandler) Rollback(c *gin.Context) {
	var req struct {
		Version string `json:"version"`
	}
	if c.Request.Body != nil && c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.Error(c, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	targetVersion := strings.TrimSpace(req.Version)

	operation := "rollback"
	if targetVersion != "" {
		operation = "rollback:" + targetVersion
	}
	operationID := buildSystemOperationID(c, operation)
	payload := gin.H{"operation_id": operationID, "version": targetVersion}
	executeAdminIdempotentJSON(c, "admin.system.rollback", payload, service.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		lock, release, err := h.acquireSystemLock(ctx, operationID)
		if err != nil {
			return nil, err
		}
		var releaseReason string
		succeeded := false
		defer func() {
			release(releaseReason, succeeded)
		}()

		var execution *service.UpdateExecution
		if targetVersion != "" {
			// 指定版本回退同样要下载完整二进制，与更新一样和请求生命周期解耦。
			rollbackCtx, cancel := systemUpdateContext(ctx)
			defer cancel()
			execution, err = h.updateSvc.RollbackToVersion(rollbackCtx, targetVersion, lock.OperationID())
		} else {
			err = h.updateSvc.Rollback()
		}
		if err != nil {
			releaseReason = "SYSTEM_ROLLBACK_FAILED"
			return nil, err
		}
		succeeded = true
		pending := execution != nil && execution.Pending
		needsRestart := !pending
		message := "Rollback completed. Please restart the service."
		if pending {
			message = "Rollback rollout scheduled. Waiting for the target service to become ready."
		}
		if execution != nil && execution.TargetVersion != "" {
			targetVersion = execution.TargetVersion
		}

		return gin.H{
			"message":        message,
			"need_restart":   needsRestart,
			"pending":        pending,
			"target_version": targetVersion,
			"version":        targetVersion,
			"operation_id":   lock.OperationID(),
		}, nil
	})
}

// RestartService restarts the systemd service
// POST /api/v1/admin/system/restart
func (h *SystemHandler) RestartService(c *gin.Context) {
	operationID := buildSystemOperationID(c, "restart")
	payload := gin.H{"operation_id": operationID}
	executeAdminIdempotentJSON(c, "admin.system.restart", payload, service.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		lock, release, err := h.acquireSystemLock(ctx, operationID)
		if err != nil {
			return nil, err
		}
		succeeded := false
		defer func() {
			release("", succeeded)
		}()

		// Schedule service restart in background after sending response
		// This ensures the client receives the success response before the service restarts
		go func() {
			// Wait a moment to ensure the response is sent
			time.Sleep(500 * time.Millisecond)
			sysutil.RestartServiceAsync()
		}()
		succeeded = true
		return gin.H{
			"message":      "Service restart initiated",
			"operation_id": lock.OperationID(),
		}, nil
	})
}

func (h *SystemHandler) acquireSystemLock(
	ctx context.Context,
	operationID string,
) (*service.SystemOperationLock, func(string, bool), error) {
	if h.lockSvc == nil {
		return nil, nil, service.ErrIdempotencyStoreUnavail
	}
	lock, err := h.lockSvc.Acquire(ctx, operationID)
	if err != nil {
		return nil, nil, err
	}
	if err := h.updateSvc.EnsureNoActiveRollout(ctx, operationID); err != nil {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.lockSvc.Release(releaseCtx, lock, false, "SYSTEM_ROLLOUT_ACTIVE")
		return nil, nil, err
	}
	release := func(reason string, succeeded bool) {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.lockSvc.Release(releaseCtx, lock, succeeded, reason)
	}
	return lock, release, nil
}

func buildSystemOperationID(c *gin.Context, operation string) string {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		seed := operation + "|" + strconv.FormatInt(time.Now().UnixNano(), 36)
		hash := service.HashIdempotencyKey(seed)
		if len(hash) > 24 {
			hash = hash[:24]
		}
		return "sysop-" + hash
	}
	actorScope := "admin:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	seed := operation + "|" + actorScope + "|" + c.FullPath() + "|" + key
	hash := service.HashIdempotencyKey(seed)
	if len(hash) > 24 {
		hash = hash[:24]
	}
	return "sysop-" + hash
}
