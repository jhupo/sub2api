package service

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrNoUpdateAvailable         = infraerrors.Conflict("ALREADY_UP_TO_DATE", "no update available; current version is latest")
	ErrRollbackVersionNotAllowed = infraerrors.BadRequest("ROLLBACK_VERSION_NOT_ALLOWED", "version is not in the allowed rollback list")
	ErrUpdateOrchestratorMissing = infraerrors.InternalServer("UPDATE_ORCHESTRATOR_MISSING", "update orchestrator is not configured")
	ErrUpdateOperationIDInvalid  = infraerrors.BadRequest("UPDATE_OPERATION_ID_INVALID", "invalid update operation id")
	ErrUpdateStatusNotFound      = infraerrors.NotFound("UPDATE_STATUS_NOT_FOUND", "update status not found")
)

const (
	updateCacheKey = "update_check_cache"
	updateCacheTTL = 1200 // 20 minutes
	githubRepo     = "jhupo/sub2api"

	// Security: allowed download domains for updates
	allowedDownloadHost = "github.com"
	allowedAssetHost    = "objects.githubusercontent.com"

	// Security: max download size (500MB)
	maxDownloadSize = 500 * 1024 * 1024

	// Rollback: expose at most the 3 most recent versions older than current
	maxRollbackVersions = 3
	// Fetch a few extra releases so filtering (current/newer/prerelease) still leaves enough candidates
	rollbackFetchPageSize = 15

	updateStrategyBinary       = "binary"
	updateStrategyRuntime      = "runtime"
	updateStrategyOrchestrated = "orchestrated"

	defaultUpdateStatusDir = "/app/data/update-status"
	maxUpdateStatusSize    = 16 * 1024
	activeRolloutMaxAge    = time.Hour

	UpdateStatusPending    = "pending"
	UpdateStatusSucceeded  = "succeeded"
	UpdateStatusRolledBack = "rolled_back"
	UpdateStatusFailed     = "failed"
)

// UpdateCache defines cache operations for update service
type UpdateCache interface {
	GetUpdateInfo(ctx context.Context) (string, error)
	SetUpdateInfo(ctx context.Context, data string, ttl time.Duration) error
}

// GitHubReleaseClient 获取 GitHub release 信息的接口
type GitHubReleaseClient interface {
	FetchLatestRelease(ctx context.Context, repo string) (*GitHubRelease, error)
	FetchRecentReleases(ctx context.Context, repo string, perPage int) ([]*GitHubRelease, error)
	DownloadFile(ctx context.Context, url, dest string, maxSize int64) error
	FetchChecksumFile(ctx context.Context, url string) ([]byte, error)
}

// UpdateService handles software updates
type UpdateService struct {
	cache          UpdateCache
	githubClient   GitHubReleaseClient
	currentVersion string
	buildType      string // "source" for manual builds, "release" for CI builds
	updateRuntime  updateRuntimeConfig
}

// updateRuntimeConfig controls how an update is applied. The default binary
// strategy preserves the existing systemd/in-place behavior. Docker or other
// multi-instance deployments should set UPDATE_STRATEGY=orchestrated and point
// UPDATE_ORCHESTRATOR at a host-side updater that owns pull, health checks,
// rolling replacement, and rollback.
type updateRuntimeConfig struct {
	strategy         string
	orchestratorPath string
	runtimePath      string
	statusDir        string
}

// NewUpdateService creates a new UpdateService
func NewUpdateService(cache UpdateCache, githubClient GitHubReleaseClient, version, buildType string) *UpdateService {
	return &UpdateService{
		cache:          cache,
		githubClient:   githubClient,
		currentVersion: version,
		buildType:      buildType,
		updateRuntime:  loadUpdateRuntimeConfig(),
	}
}

// UpdateInfo contains update information
type UpdateInfo struct {
	CurrentVersion string       `json:"current_version"`
	LatestVersion  string       `json:"latest_version"`
	HasUpdate      bool         `json:"has_update"`
	ReleaseInfo    *ReleaseInfo `json:"release_info,omitempty"`
	Cached         bool         `json:"cached"`
	Warning        string       `json:"warning,omitempty"`
	BuildType      string       `json:"build_type"` // "source" or "release"
	UpdateStrategy string       `json:"update_strategy"`
}

// ReleaseInfo contains GitHub release details
type ReleaseInfo struct {
	Name        string  `json:"name"`
	Body        string  `json:"body"`
	PublishedAt string  `json:"published_at"`
	HTMLURL     string  `json:"html_url"`
	Assets      []Asset `json:"assets,omitempty"`
}

// Asset represents a release asset
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
	Size        int64  `json:"size"`
}

// GitHubRelease represents GitHub API response
type GitHubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Body        string        `json:"body"`
	PublishedAt string        `json:"published_at"`
	HTMLURL     string        `json:"html_url"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	Assets      []GitHubAsset `json:"assets"`
}

// UpdateExecution describes how an accepted update will finish.
type UpdateExecution struct {
	TargetVersion string
	Pending       bool
}

// UpdateRolloutStatus is initialized before the orchestrator starts and then
// updated by the rollout process as readiness or rollback completes.
type UpdateRolloutStatus struct {
	OperationID    string `json:"operation_id"`
	Status         string `json:"status"`
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
	Reason         string `json:"reason,omitempty"`
}

// RollbackVersion describes a release version the system can roll back to
type RollbackVersion struct {
	Version     string `json:"version"` // without "v" prefix, e.g. "0.1.146"
	PublishedAt string `json:"published_at"`
	HTMLURL     string `json:"html_url"`
}

type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

func loadUpdateRuntimeConfig() updateRuntimeConfig {
	strategy := strings.ToLower(strings.TrimSpace(os.Getenv("UPDATE_STRATEGY")))
	if strategy == "" {
		strategy = updateStrategyBinary
	}
	if strategy != updateStrategyBinary && strategy != updateStrategyRuntime && strategy != updateStrategyOrchestrated {
		strategy = updateStrategyBinary
	}
	statusDir := strings.TrimSpace(os.Getenv("SUB2API_UPDATE_STATUS_DIR"))
	if statusDir == "" {
		statusDir = defaultUpdateStatusDir
	}
	return updateRuntimeConfig{
		strategy:         strategy,
		orchestratorPath: strings.TrimSpace(os.Getenv("UPDATE_ORCHESTRATOR")),
		runtimePath:      strings.TrimSpace(os.Getenv("UPDATE_RUNTIME_BINARY_PATH")),
		statusDir:        statusDir,
	}
}

// CheckUpdate checks for available updates
func (s *UpdateService) CheckUpdate(ctx context.Context, force bool) (*UpdateInfo, error) {
	// Try cache first
	if !force {
		if cached, err := s.getFromCache(ctx); err == nil && cached != nil {
			return cached, nil
		}
	}

	// Fetch from GitHub
	info, err := s.fetchLatestRelease(ctx)
	if err != nil {
		// Return cached on error
		if cached, cacheErr := s.getFromCache(ctx); cacheErr == nil && cached != nil {
			cached.Warning = "Using cached data: " + err.Error()
			return cached, nil
		}
		return &UpdateInfo{
			CurrentVersion: s.currentVersion,
			LatestVersion:  s.currentVersion,
			HasUpdate:      false,
			Warning:        err.Error(),
			BuildType:      s.buildType,
			UpdateStrategy: s.updateRuntime.strategy,
		}, nil
	}

	// Cache result
	s.saveToCache(ctx, info)
	return info, nil
}

// PerformUpdate downloads and applies the update
// Uses atomic file replacement pattern for safe in-place updates
func (s *UpdateService) PerformUpdate(ctx context.Context, operationID string) (*UpdateExecution, error) {
	info, err := s.CheckUpdate(ctx, true)
	if err != nil {
		return nil, normalizeUpdateError(err)
	}

	if !info.HasUpdate {
		return nil, ErrNoUpdateAvailable
	}

	if s.updateRuntime.strategy == updateStrategyOrchestrated {
		if err := normalizeUpdateError(s.performOrchestratedUpdate(ctx, info, operationID)); err != nil {
			return nil, err
		}
		return &UpdateExecution{TargetVersion: info.LatestVersion, Pending: true}, nil
	}

	if err := normalizeUpdateError(s.applyReleaseAssets(ctx, info.ReleaseInfo.Assets)); err != nil {
		return nil, err
	}
	return &UpdateExecution{TargetVersion: info.LatestVersion}, nil
}

// normalizeUpdateError keeps update failures actionable for administrators.
// The generic response layer intentionally hides raw errors, so untyped
// download/orchestrator failures would otherwise become only "internal error".
func normalizeUpdateError(err error) error {
	if err == nil || infraerrors.Reason(err) != "" {
		return err
	}
	return infraerrors.InternalServer("UPDATE_FAILED", err.Error()).WithCause(err)
}

// NeedsRestart reports whether the caller must restart the current process.
// The orchestrated strategy hands restart and readiness verification to its
// rollout helper, so sending a second restart request would race that helper.
func (s *UpdateService) NeedsRestart() bool {
	return s.updateRuntime.strategy != updateStrategyOrchestrated
}

// performOrchestratedUpdate records the lease before starting the host-side
// runner, then returns immediately so the client can follow the operation ID.
// Docker access remains outside the application process unless this strategy
// is explicitly configured.
func (s *UpdateService) performOrchestratedUpdate(_ context.Context, info *UpdateInfo, operationID string) error {
	if !validUpdateOperationID(operationID) {
		return ErrUpdateOperationIDInvalid
	}
	path := s.updateRuntime.orchestratorPath
	if path == "" {
		return ErrUpdateOrchestratorMissing
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("update orchestrator must be an absolute path: %s", path)
	}
	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("update orchestrator unavailable: %w", err)
	}
	if stat.IsDir() {
		return fmt.Errorf("update orchestrator is a directory: %s", path)
	}

	releaseURL := ""
	if info.ReleaseInfo != nil {
		releaseURL = info.ReleaseInfo.HTMLURL
	}
	status := UpdateRolloutStatus{
		OperationID:    operationID,
		Status:         UpdateStatusPending,
		CurrentVersion: info.CurrentVersion,
		TargetVersion:  info.LatestVersion,
	}
	if err := s.writeUpdateStatus(status); err != nil {
		return err
	}

	cmd := exec.Command(path,
		"--current-version", info.CurrentVersion,
		"--target-version", info.LatestVersion,
		"--release-url", releaseURL,
		"--operation-id", operationID,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		status.Status = UpdateStatusFailed
		status.Reason = "orchestrator_start_failed"
		_ = s.writeUpdateStatus(status)
		return fmt.Errorf("orchestrated update failed to start: %w", err)
	}
	go s.waitForOrchestrator(cmd, status)
	return nil
}

func (s *UpdateService) waitForOrchestrator(cmd *exec.Cmd, status UpdateRolloutStatus) {
	if err := cmd.Wait(); err == nil {
		return
	}
	current, err := s.GetUpdateStatus(context.Background(), status.OperationID)
	if err == nil && current.Status != UpdateStatusPending {
		return
	}
	status.Status = UpdateStatusFailed
	status.Reason = "orchestrator_failed"
	_ = s.writeUpdateStatus(status)
}

func (s *UpdateService) updateStatusPath(operationID string) (string, error) {
	dir := strings.TrimSpace(s.updateRuntime.statusDir)
	cleanDir := filepath.Clean(dir)
	if dir == "" || !filepath.IsAbs(dir) || cleanDir != dir {
		return "", infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status directory is invalid")
	}
	if info, err := os.Lstat(cleanDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status directory is invalid")
		}
	} else if !os.IsNotExist(err) {
		return "", infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	return filepath.Join(cleanDir, operationID+".json"), nil
}

func (s *UpdateService) writeUpdateStatus(status UpdateRolloutStatus) error {
	if !validUpdateOperationID(status.OperationID) || !validUpdateStatus(status.Status) {
		return infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status is invalid")
	}
	path, err := s.updateStatusPath(status.OperationID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	if info, err := os.Lstat(filepath.Dir(path)); err != nil {
		return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status directory is invalid")
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+status.OperationID+".tmp-*")
	if err != nil {
		return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	tempPath := file.Name()
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	if err := json.NewEncoder(file).Encode(status); err != nil {
		return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	if err := file.Sync(); err != nil {
		return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	if err := file.Close(); err != nil {
		return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	removeTemp = false
	return nil
}

// GetUpdateStatus reads the final state written to the shared data volume by
// the detached orchestrator helper. Operation IDs are validated before they
// are used as filenames.
func (s *UpdateService) GetUpdateStatus(_ context.Context, operationID string) (*UpdateRolloutStatus, error) {
	if !validUpdateOperationID(operationID) {
		return nil, ErrUpdateOperationIDInvalid
	}

	path, err := s.updateStatusPath(operationID)
	if err != nil {
		return nil, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrUpdateStatusNotFound
		}
		return nil, infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status is invalid")
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrUpdateStatusNotFound
		}
		return nil, infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		return nil, infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status is invalid").WithCause(err)
	}
	currentInfo, err := os.Lstat(path)
	if err != nil || currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.Mode().IsRegular() || !os.SameFile(openedInfo, currentInfo) {
		return nil, infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status is invalid").WithCause(err)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxUpdateStatusSize+1))
	if err != nil {
		return nil, infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}
	if len(data) > maxUpdateStatusSize {
		return nil, infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status is invalid")
	}

	var status UpdateRolloutStatus
	if err := json.Unmarshal(data, &status); err != nil || status.OperationID != operationID || !validUpdateStatus(status.Status) {
		return nil, infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status is invalid").WithCause(err)
	}
	if status.Status == UpdateStatusPending && time.Since(currentInfo.ModTime()) > activeRolloutMaxAge {
		status.Status = UpdateStatusFailed
		status.Reason = "lease_expired"
	}
	return &status, nil
}

// EnsureNoActiveRollout extends the existing system-operation lock across a
// detached container replacement. Pending files live on the shared data volume
// and remain visible after the request-serving process is recreated.
func (s *UpdateService) EnsureNoActiveRollout(ctx context.Context, operationID string) error {
	if !validUpdateOperationID(operationID) {
		return ErrUpdateOperationIDInvalid
	}

	statusPath, err := s.updateStatusPath(operationID)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Dir(statusPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		candidateID := strings.TrimSuffix(entry.Name(), ".json")
		if !validUpdateOperationID(candidateID) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infraerrors.InternalServer("UPDATE_STATUS_UNAVAILABLE", "update status is unavailable").WithCause(infoErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return infraerrors.InternalServer("UPDATE_STATUS_INVALID", "update status is invalid")
		}
		status, statusErr := s.GetUpdateStatus(ctx, candidateID)
		if statusErr != nil {
			if errors.Is(statusErr, ErrUpdateStatusNotFound) {
				continue
			}
			return statusErr
		}
		if status.Status == UpdateStatusPending {
			return ErrSystemOperationBusy.WithMetadata(map[string]string{"operation_id": candidateID})
		}
	}
	return nil
}

func validUpdateOperationID(operationID string) bool {
	if len(operationID) < len("sysop-x") || len(operationID) > 128 || !strings.HasPrefix(operationID, "sysop-") {
		return false
	}
	for _, char := range operationID {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func validUpdateStatus(status string) bool {
	switch status {
	case UpdateStatusPending, UpdateStatusSucceeded, UpdateStatusRolledBack, UpdateStatusFailed:
		return true
	default:
		return false
	}
}

// applyReleaseAssets downloads the platform archive from the given release assets,
// verifies its checksum, and atomically swaps the running binary.
// Shared by PerformUpdate (latest) and RollbackToVersion (specific older version).
func (s *UpdateService) applyReleaseAssets(ctx context.Context, releaseAssets []Asset) error {
	// Find matching archive and checksum for current platform
	archiveName := s.getArchiveName()
	var downloadURL string
	var checksumURL string

	for _, asset := range releaseAssets {
		if strings.Contains(asset.Name, archiveName) && !strings.HasSuffix(asset.Name, ".txt") {
			downloadURL = asset.DownloadURL
		}
		if asset.Name == "checksums.txt" {
			checksumURL = asset.DownloadURL
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no compatible release found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// SECURITY: Validate download URL is from trusted domain
	if err := validateDownloadURL(downloadURL); err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	if checksumURL != "" {
		if err := validateDownloadURL(checksumURL); err != nil {
			return fmt.Errorf("invalid checksum URL: %w", err)
		}
	}

	// Runtime deployments may expose a writable binary path mounted outside
	// the image. Otherwise retain the existing executable replacement behavior.
	exePath, err := s.updateTargetPath()
	if err != nil {
		return err
	}

	exeDir := filepath.Dir(exePath)

	// Create temp directory in the SAME directory as executable
	// This ensures os.Rename is atomic (same filesystem)
	tempDir, err := os.MkdirTemp(exeDir, ".sub2api-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	// Download archive
	archivePath := filepath.Join(tempDir, filepath.Base(downloadURL))
	if err := s.downloadFile(ctx, downloadURL, archivePath); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Verify checksum if available
	if checksumURL != "" {
		if err := s.verifyChecksum(ctx, archivePath, checksumURL); err != nil {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
	}

	// Extract binary from archive
	newBinaryPath := filepath.Join(tempDir, "sub2api")
	if err := s.extractBinary(archivePath, newBinaryPath); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// Set executable permission before replacement
	if err := os.Chmod(newBinaryPath, 0755); err != nil {
		return fmt.Errorf("chmod failed: %w", err)
	}

	// Atomic replacement using rename pattern:
	// 1. Rename current -> backup (atomic on Unix)
	// 2. Rename new -> current (atomic on Unix, same filesystem)
	// If step 2 fails, restore backup
	backupPath := exePath + ".backup"

	// Remove old backup if exists
	_ = os.Remove(backupPath)

	// Step 1: Move current binary to backup
	if err := os.Rename(exePath, backupPath); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	// Step 2: Move new binary to target location (atomic, same filesystem)
	if err := os.Rename(newBinaryPath, exePath); err != nil {
		// Restore backup on failure
		if restoreErr := os.Rename(backupPath, exePath); restoreErr != nil {
			return fmt.Errorf("replace failed and restore failed: %w (restore error: %v)", err, restoreErr)
		}
		return fmt.Errorf("replace failed (restored backup): %w", err)
	}

	// Success - backup file is kept for rollback capability
	// It will be cleaned up on next successful update
	return nil
}

func (s *UpdateService) updateTargetPath() (string, error) {
	if runtimePath := strings.TrimSpace(s.updateRuntime.runtimePath); runtimePath != "" {
		if !filepath.IsAbs(runtimePath) {
			return "", fmt.Errorf("UPDATE_RUNTIME_BINARY_PATH must be absolute: %s", runtimePath)
		}
		return filepath.Clean(runtimePath), nil
	}

	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve symlinks: %w", err)
	}
	return exePath, nil
}

// Rollback restores the previous version
func (s *UpdateService) Rollback() error {
	if s.updateRuntime.strategy == updateStrategyOrchestrated {
		return infraerrors.BadRequest("ROLLBACK_VERSION_REQUIRED", "select a release version for Docker rollback")
	}

	exePath, err := s.updateTargetPath()
	if err != nil {
		return err
	}

	backupFile := exePath + ".backup"
	if _, err := os.Stat(backupFile); os.IsNotExist(err) {
		return fmt.Errorf("no backup found")
	}

	// Replace current with backup
	if err := os.Rename(backupFile, exePath); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}

	return nil
}

// ListRollbackVersions returns up to maxRollbackVersions release versions that are
// strictly older than the current version (the current version itself is excluded),
// newest first. Draft and prerelease entries are skipped.
func (s *UpdateService) ListRollbackVersions(ctx context.Context) ([]RollbackVersion, error) {
	releases, err := s.fetchRollbackCandidates(ctx)
	if err != nil {
		return nil, err
	}

	versions := make([]RollbackVersion, 0, len(releases))
	for _, r := range releases {
		versions = append(versions, RollbackVersion{
			Version:     strings.TrimPrefix(r.TagName, "v"),
			PublishedAt: r.PublishedAt,
			HTMLURL:     r.HTMLURL,
		})
	}
	return versions, nil
}

// RollbackToVersion downloads and installs a specific older version.
// The target must be one of the versions returned by ListRollbackVersions;
// anything else (including the current version) is rejected.
func (s *UpdateService) RollbackToVersion(ctx context.Context, version, operationID string) (*UpdateExecution, error) {
	target := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if target == "" {
		return nil, ErrRollbackVersionNotAllowed
	}

	releases, err := s.fetchRollbackCandidates(ctx)
	if err != nil {
		return nil, err
	}

	var match *GitHubRelease
	for _, r := range releases {
		if strings.TrimPrefix(r.TagName, "v") == target {
			match = r
			break
		}
	}
	if match == nil {
		return nil, ErrRollbackVersionNotAllowed
	}
	if s.updateRuntime.strategy == updateStrategyOrchestrated {
		if err := s.performOrchestratedUpdate(ctx, &UpdateInfo{
			CurrentVersion: s.currentVersion,
			LatestVersion:  strings.TrimPrefix(match.TagName, "v"),
			ReleaseInfo: &ReleaseInfo{
				Name:        match.Name,
				Body:        match.Body,
				PublishedAt: match.PublishedAt,
				HTMLURL:     match.HTMLURL,
			},
			BuildType:      s.buildType,
			UpdateStrategy: s.updateRuntime.strategy,
		}, operationID); err != nil {
			return nil, err
		}
		return &UpdateExecution{TargetVersion: target, Pending: true}, nil
	}

	assets := make([]Asset, len(match.Assets))
	for i, a := range match.Assets {
		assets[i] = Asset{
			Name:        a.Name,
			DownloadURL: a.BrowserDownloadURL,
			Size:        a.Size,
		}
	}

	if err := s.applyReleaseAssets(ctx, assets); err != nil {
		return nil, err
	}
	return &UpdateExecution{TargetVersion: target}, nil
}

// fetchRollbackCandidates fetches recent releases and keeps the newest
// maxRollbackVersions entries strictly older than the current version.
func (s *UpdateService) fetchRollbackCandidates(ctx context.Context) ([]*GitHubRelease, error) {
	releases, err := s.githubClient.FetchRecentReleases(ctx, githubRepo, rollbackFetchPageSize)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(releases))
	candidates := make([]*GitHubRelease, 0, maxRollbackVersions)
	for _, r := range releases {
		if r == nil || r.Draft || r.Prerelease {
			continue
		}
		v := strings.TrimPrefix(r.TagName, "v")
		if v == "" || seen[v] {
			continue
		}
		// Only versions strictly older than current (also excludes current itself)
		if compareVersions(v, s.currentVersion) >= 0 {
			continue
		}
		seen[v] = true
		candidates = append(candidates, r)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return compareVersions(
			strings.TrimPrefix(candidates[i].TagName, "v"),
			strings.TrimPrefix(candidates[j].TagName, "v"),
		) > 0
	})

	if len(candidates) > maxRollbackVersions {
		candidates = candidates[:maxRollbackVersions]
	}
	return candidates, nil
}

func (s *UpdateService) fetchLatestRelease(ctx context.Context) (*UpdateInfo, error) {
	release, err := s.githubClient.FetchLatestRelease(ctx, githubRepo)
	if err != nil {
		return nil, err
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")

	assets := make([]Asset, len(release.Assets))
	for i, a := range release.Assets {
		assets[i] = Asset{
			Name:        a.Name,
			DownloadURL: a.BrowserDownloadURL,
			Size:        a.Size,
		}
	}

	return &UpdateInfo{
		CurrentVersion: s.currentVersion,
		LatestVersion:  latestVersion,
		HasUpdate:      compareVersions(s.currentVersion, latestVersion) < 0,
		ReleaseInfo: &ReleaseInfo{
			Name:        release.Name,
			Body:        release.Body,
			PublishedAt: release.PublishedAt,
			HTMLURL:     release.HTMLURL,
			Assets:      assets,
		},
		Cached:         false,
		BuildType:      s.buildType,
		UpdateStrategy: s.updateRuntime.strategy,
	}, nil
}

func (s *UpdateService) downloadFile(ctx context.Context, downloadURL, dest string) error {
	return s.githubClient.DownloadFile(ctx, downloadURL, dest, maxDownloadSize)
}

func (s *UpdateService) getArchiveName() string {
	osName := runtime.GOOS
	arch := runtime.GOARCH
	return fmt.Sprintf("%s_%s", osName, arch)
}

// validateDownloadURL checks if the URL is from an allowed domain
// SECURITY: This prevents SSRF and ensures downloads only come from trusted GitHub domains
func validateDownloadURL(rawURL string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Must be HTTPS
	if parsedURL.Scheme != "https" {
		return fmt.Errorf("only HTTPS URLs are allowed")
	}

	// Check against allowed hosts
	host := parsedURL.Host
	// GitHub release URLs can be from github.com or objects.githubusercontent.com
	if host != allowedDownloadHost &&
		!strings.HasSuffix(host, "."+allowedDownloadHost) &&
		host != allowedAssetHost &&
		!strings.HasSuffix(host, "."+allowedAssetHost) {
		return fmt.Errorf("download from untrusted host: %s", host)
	}

	return nil
}

func (s *UpdateService) verifyChecksum(ctx context.Context, filePath, checksumURL string) error {
	// Download checksums file
	checksumData, err := s.githubClient.FetchChecksumFile(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("failed to download checksums: %w", err)
	}

	// Calculate file hash
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actualHash := hex.EncodeToString(h.Sum(nil))

	// Find expected hash in checksums file
	fileName := filepath.Base(filePath)
	scanner := bufio.NewScanner(strings.NewReader(string(checksumData)))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == fileName {
			if parts[0] == actualHash {
				return nil
			}
			return fmt.Errorf("checksum mismatch: expected %s, got %s", parts[0], actualHash)
		}
	}

	return fmt.Errorf("checksum not found for %s", fileName)
}

func (s *UpdateService) extractBinary(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var reader io.Reader = f

	// Handle gzip compression
	if strings.HasSuffix(archivePath, ".gz") || strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz") {
		gzr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer func() { _ = gzr.Close() }()
		reader = gzr
	}

	// Handle tar archive
	if strings.Contains(archivePath, ".tar") {
		tr := tar.NewReader(reader)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}

			// SECURITY: Prevent Zip Slip / Path Traversal attack
			// Only allow files with safe base names, no directory traversal
			baseName := filepath.Base(hdr.Name)

			// Check for path traversal attempts
			if strings.Contains(hdr.Name, "..") {
				return fmt.Errorf("path traversal attempt detected: %s", hdr.Name)
			}

			// Validate the entry is a regular file
			if hdr.Typeflag != tar.TypeReg {
				continue // Skip directories and special files
			}

			// Only extract the specific binary we need
			if baseName == "sub2api" || baseName == "sub2api.exe" {
				// Additional security: limit file size (max 500MB)
				const maxBinarySize = 500 * 1024 * 1024
				if hdr.Size > maxBinarySize {
					return fmt.Errorf("binary too large: %d bytes (max %d)", hdr.Size, maxBinarySize)
				}

				out, err := os.Create(destPath)
				if err != nil {
					return err
				}

				// Use LimitReader to prevent decompression bombs
				limited := io.LimitReader(tr, maxBinarySize)
				if _, err := io.Copy(out, limited); err != nil {
					_ = out.Close()
					return err
				}
				if err := out.Close(); err != nil {
					return err
				}
				return nil
			}
		}
		return fmt.Errorf("binary not found in archive")
	}

	// Direct copy for non-tar files (with size limit)
	const maxBinarySize = 500 * 1024 * 1024
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}

	limited := io.LimitReader(reader, maxBinarySize)
	if _, err := io.Copy(out, limited); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func (s *UpdateService) getFromCache(ctx context.Context) (*UpdateInfo, error) {
	data, err := s.cache.GetUpdateInfo(ctx)
	if err != nil {
		return nil, err
	}

	var cached struct {
		Latest      string       `json:"latest"`
		ReleaseInfo *ReleaseInfo `json:"release_info"`
		Timestamp   int64        `json:"timestamp"`
	}
	if err := json.Unmarshal([]byte(data), &cached); err != nil {
		return nil, err
	}

	if time.Now().Unix()-cached.Timestamp > updateCacheTTL {
		return nil, fmt.Errorf("cache expired")
	}

	return &UpdateInfo{
		CurrentVersion: s.currentVersion,
		LatestVersion:  cached.Latest,
		HasUpdate:      compareVersions(s.currentVersion, cached.Latest) < 0,
		ReleaseInfo:    cached.ReleaseInfo,
		Cached:         true,
		BuildType:      s.buildType,
		UpdateStrategy: s.updateRuntime.strategy,
	}, nil
}

func (s *UpdateService) saveToCache(ctx context.Context, info *UpdateInfo) {
	cacheData := struct {
		Latest      string       `json:"latest"`
		ReleaseInfo *ReleaseInfo `json:"release_info"`
		Timestamp   int64        `json:"timestamp"`
	}{
		Latest:      info.LatestVersion,
		ReleaseInfo: info.ReleaseInfo,
		Timestamp:   time.Now().Unix(),
	}

	data, _ := json.Marshal(cacheData)
	_ = s.cache.SetUpdateInfo(ctx, string(data), time.Duration(updateCacheTTL)*time.Second)
}

// compareVersions compares two semantic versions
func compareVersions(current, latest string) int {
	currentParts := parseVersion(current)
	latestParts := parseVersion(latest)

	for i := 0; i < 3; i++ {
		if currentParts[i] < latestParts[i] {
			return -1
		}
		if currentParts[i] > latestParts[i] {
			return 1
		}
	}
	return 0
}

func parseVersion(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	result := [3]int{0, 0, 0}
	for i := 0; i < len(parts) && i < 3; i++ {
		if parsed, err := strconv.Atoi(parts[i]); err == nil {
			result[i] = parsed
		}
	}
	return result
}
