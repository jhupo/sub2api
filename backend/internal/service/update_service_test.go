//go:build unit

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type updateServiceCacheStub struct {
	data string
}

func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

type updateServiceGitHubClientStub struct {
	release        *GitHubRelease
	recentReleases []*GitHubRelease
	recentErr      error
}

func (s *updateServiceGitHubClientStub) FetchLatestRelease(context.Context, string) (*GitHubRelease, error) {
	return s.release, nil
}

func (s *updateServiceGitHubClientStub) FetchRecentReleases(context.Context, string, int) ([]*GitHubRelease, error) {
	return s.recentReleases, s.recentErr
}

func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &GitHubRelease{
				TagName: "v0.1.132",
				Name:    "v0.1.132",
			},
		},
		"0.1.132",
		"release",
	)

	_, err := svc.PerformUpdate(context.Background(), "sysop-no-update")

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}

func TestUpdateServiceOrchestratedStrategyRequiresConfiguredRunner(t *testing.T) {
	t.Setenv("UPDATE_STRATEGY", "orchestrated")
	t.Setenv("UPDATE_ORCHESTRATOR", "")

	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &GitHubRelease{
				TagName: "v0.1.133",
				Name:    "v0.1.133",
			},
		},
		"0.1.132",
		"release",
	)

	_, err := svc.PerformUpdate(context.Background(), "sysop-runner-required")

	require.ErrorIs(t, err, ErrUpdateOrchestratorMissing)
}

func TestUpdateServiceOrchestratedStrategyReturnsAfterStartingRunner(t *testing.T) {
	t.Setenv("UPDATE_STRATEGY", "orchestrated")
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", t.TempDir())
	runner, err := exec.LookPath("false")
	if err != nil {
		runner, err = exec.LookPath("where.exe")
	}
	require.NoError(t, err)
	t.Setenv("UPDATE_ORCHESTRATOR", runner)

	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.1.133", Name: "v0.1.133"}},
		"0.1.132",
		"release",
	)

	started := time.Now()
	execution, err := svc.PerformUpdate(context.Background(), "sysop-async-runner")

	require.NoError(t, err)
	require.True(t, execution.Pending)
	require.Less(t, time.Since(started), 5*time.Second)
	require.Eventually(t, func() bool {
		status, statusErr := svc.GetUpdateStatus(context.Background(), "sysop-async-runner")
		return statusErr == nil && status.Status == UpdateStatusFailed
	}, 5*time.Second, 10*time.Millisecond)
}

func TestUpdateServiceNeedsRestartDependsOnStrategy(t *testing.T) {
	t.Setenv("UPDATE_STRATEGY", "orchestrated")
	orchestrated := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.132", "release")
	require.False(t, orchestrated.NeedsRestart())

	t.Setenv("UPDATE_STRATEGY", "binary")
	binary := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.132", "release")
	require.True(t, binary.NeedsRestart())
}

func TestNormalizeUpdateErrorPreservesActionableMessage(t *testing.T) {
	err := normalizeUpdateError(errors.New("checksum mismatch for release asset"))

	require.Equal(t, "UPDATE_FAILED", infraerrors.Reason(err))
	require.Equal(t, "checksum mismatch for release asset", infraerrors.Message(err))
}

func TestOrchestratedRollbackRequiresReleaseVersion(t *testing.T) {
	t.Setenv("UPDATE_STRATEGY", "orchestrated")

	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.132", "release")
	err := svc.Rollback()

	require.Equal(t, "ROLLBACK_VERSION_REQUIRED", infraerrors.Reason(err))
}

func newRollbackTestService(current string, releases []*GitHubRelease) *UpdateService {
	return NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
	)
}

func TestUpdateServiceListRollbackVersionsFiltersAndCaps(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148", PublishedAt: "2026-07-09T00:00:00Z"},                       // newer than current: excluded
		{TagName: "v0.1.147", PublishedAt: "2026-07-08T00:00:00Z"},                       // current: excluded
		{TagName: "v0.1.146-rc1", PublishedAt: "2026-07-07T12:00:00Z", Prerelease: true}, // prerelease: excluded
		{TagName: "v0.1.146", PublishedAt: "2026-07-07T00:00:00Z"},
		{TagName: "v0.1.145", PublishedAt: "2026-07-06T00:00:00Z", Draft: true}, // draft: excluded
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"},
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"}, // duplicate: excluded
		{TagName: "v0.1.143", PublishedAt: "2026-07-04T00:00:00Z"},
		{TagName: "v0.1.142", PublishedAt: "2026-07-03T00:00:00Z"}, // beyond cap of 3: excluded
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.144", versions[1].Version)
	require.Equal(t, "0.1.143", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsSortsUnorderedInput(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.144"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.145", versions[1].Version)
	require.Equal(t, "0.1.144", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsEmptyWhenNoneOlder(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.148"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Empty(t, versions)
}

func TestUpdateServiceListRollbackVersionsPropagatesFetchError(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentErr: errors.New("github unavailable")},
		"0.1.147",
		"release",
	)

	_, err := svc.ListRollbackVersions(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "github unavailable")
}

func TestUpdateServiceRollbackToVersionRejectsDisallowedTargets(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148"},
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
		{TagName: "v0.1.144"},
		{TagName: "v0.1.143"},
		{TagName: "v0.1.142"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	for _, target := range []string{
		"",         // empty
		"0.1.147",  // current version
		"v0.1.147", // current version with prefix
		"0.1.148",  // newer than current
		"0.1.142",  // older than the 3 most recent
		"9.9.9",    // nonexistent
	} {
		_, err := svc.RollbackToVersion(context.Background(), target, "sysop-rollback-rejected")
		require.ErrorIs(t, err, ErrRollbackVersionNotAllowed, "target %q should be rejected", target)
	}
}

func TestUpdateServiceRollbackToVersionAcceptsVPrefix(t *testing.T) {
	// No platform asset in the release: the target passes the allowlist check
	// and fails later at asset lookup, proving the version itself was accepted.
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	_, err := svc.RollbackToVersion(context.Background(), "v0.1.146", "sysop-rollback-accepted")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRollbackVersionNotAllowed)
	require.Contains(t, err.Error(), "no compatible release found")
}

func TestUpdateServiceGetUpdateStatus(t *testing.T) {
	statusDir := t.TempDir()
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	operationID := "sysop-update123"
	require.NoError(t, os.WriteFile(filepath.Join(statusDir, operationID+".json"), []byte(`{
		"operation_id":"sysop-update123",
		"status":"succeeded",
		"current_version":"0.1.241",
		"target_version":"0.1.242"
	}`), 0o600))
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	status, err := svc.GetUpdateStatus(context.Background(), operationID)

	require.NoError(t, err)
	require.Equal(t, UpdateStatusSucceeded, status.Status)
	require.Equal(t, "0.1.242", status.TargetVersion)
}

func TestUpdateServiceGetUpdateStatusRejectsUnsafeOperationID(t *testing.T) {
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	_, err := svc.GetUpdateStatus(context.Background(), "sysop-../../config")

	require.ErrorIs(t, err, ErrUpdateOperationIDInvalid)
}

func TestUpdateServiceGetUpdateStatusReturnsNotFound(t *testing.T) {
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", t.TempDir())
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	_, err := svc.GetUpdateStatus(context.Background(), "sysop-missing")

	require.ErrorIs(t, err, ErrUpdateStatusNotFound)
}

func TestUpdateServiceGetUpdateStatusRejectsMismatchedFile(t *testing.T) {
	statusDir := t.TempDir()
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	operationID := "sysop-requested"
	require.NoError(t, os.WriteFile(filepath.Join(statusDir, operationID+".json"), []byte(
		`{"operation_id":"sysop-different","status":"succeeded"}`,
	), 0o600))
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	_, err := svc.GetUpdateStatus(context.Background(), operationID)

	require.Equal(t, "UPDATE_STATUS_INVALID", infraerrors.Reason(err))
}

func TestUpdateServiceGetUpdateStatusRejectsSymlink(t *testing.T) {
	statusDir := t.TempDir()
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	target := filepath.Join(statusDir, "target.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"operation_id":"sysop-linked","status":"succeeded"}`), 0o600))
	path := filepath.Join(statusDir, "sysop-linked.json")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	_, err := svc.GetUpdateStatus(context.Background(), "sysop-linked")

	require.Equal(t, "UPDATE_STATUS_INVALID", infraerrors.Reason(err))
}

func TestUpdateServiceGetUpdateStatusRejectsNonRegularFile(t *testing.T) {
	statusDir := t.TempDir()
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	require.NoError(t, os.Mkdir(filepath.Join(statusDir, "sysop-directory.json"), 0o700))
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	_, err := svc.GetUpdateStatus(context.Background(), "sysop-directory")

	require.Equal(t, "UPDATE_STATUS_INVALID", infraerrors.Reason(err))
}

func TestUpdateServiceGetUpdateStatusRejectsUncleanStatusDirectory(t *testing.T) {
	uncleanDir := filepath.Join(t.TempDir(), "nested") + string(os.PathSeparator) + ".."
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", uncleanDir)
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	_, err := svc.GetUpdateStatus(context.Background(), "sysop-unclean")

	require.Equal(t, "UPDATE_STATUS_INVALID", infraerrors.Reason(err))
}

func TestUpdateServiceGetUpdateStatusRejectsSymlinkStatusDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	statusDir := filepath.Join(root, "linked")
	if err := os.Symlink(target, statusDir); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	_, err := svc.GetUpdateStatus(context.Background(), "sysop-linked-directory")

	require.Equal(t, "UPDATE_STATUS_INVALID", infraerrors.Reason(err))
}

func TestUpdateServiceGetUpdateStatusRejectsNonDirectoryStatusPath(t *testing.T) {
	statusDir := filepath.Join(t.TempDir(), "status-file")
	require.NoError(t, os.WriteFile(statusDir, nil, 0o600))
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	_, err := svc.GetUpdateStatus(context.Background(), "sysop-status-file")

	require.Equal(t, "UPDATE_STATUS_INVALID", infraerrors.Reason(err))
}

func TestUpdateServiceEnsureNoActiveRolloutRejectsFreshPending(t *testing.T) {
	statusDir := t.TempDir()
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	require.NoError(t, os.WriteFile(filepath.Join(statusDir, "sysop-active.json"), []byte(
		`{"operation_id":"sysop-active","status":"pending","current_version":"0.1.241","target_version":"0.1.242"}`,
	), 0o600))
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	err := svc.EnsureNoActiveRollout(context.Background(), "sysop-new")

	require.ErrorIs(t, err, ErrSystemOperationBusy)
}

func TestUpdateServiceEnsureNoActiveRolloutRejectsFreshPendingWithSameOperationID(t *testing.T) {
	statusDir := t.TempDir()
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	require.NoError(t, os.WriteFile(filepath.Join(statusDir, "sysop-replayed.json"), []byte(
		`{"operation_id":"sysop-replayed","status":"pending","current_version":"0.1.241","target_version":"0.1.242"}`,
	), 0o600))
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	err := svc.EnsureNoActiveRollout(context.Background(), "sysop-replayed")

	require.ErrorIs(t, err, ErrSystemOperationBusy)
}

func TestUpdateServiceEnsureNoActiveRolloutRejectsNonRegularStatusFile(t *testing.T) {
	statusDir := t.TempDir()
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	require.NoError(t, os.Mkdir(filepath.Join(statusDir, "sysop-directory.json"), 0o700))
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	err := svc.EnsureNoActiveRollout(context.Background(), "sysop-new")

	require.Equal(t, "UPDATE_STATUS_INVALID", infraerrors.Reason(err))
}

func TestUpdateServiceEnsureNoActiveRolloutAllowsTerminalStates(t *testing.T) {
	for _, status := range []string{UpdateStatusSucceeded, UpdateStatusRolledBack, UpdateStatusFailed} {
		t.Run(status, func(t *testing.T) {
			statusDir := t.TempDir()
			t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
			require.NoError(t, os.WriteFile(filepath.Join(statusDir, "sysop-finished.json"), []byte(
				`{"operation_id":"sysop-finished","status":"`+status+`","current_version":"0.1.241","target_version":"0.1.242"}`,
			), 0o600))
			svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

			require.NoError(t, svc.EnsureNoActiveRollout(context.Background(), "sysop-new"))
		})
	}
}

func TestUpdateServiceEnsureNoActiveRolloutAllowsStalePending(t *testing.T) {
	statusDir := t.TempDir()
	t.Setenv("SUB2API_UPDATE_STATUS_DIR", statusDir)
	path := filepath.Join(statusDir, "sysop-stale.json")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"operation_id":"sysop-stale","status":"pending","current_version":"0.1.241","target_version":"0.1.242"}`,
	), 0o600))
	staleTime := time.Now().Add(-activeRolloutMaxAge - time.Minute)
	require.NoError(t, os.Chtimes(path, staleTime, staleTime))
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.241", "release")

	status, err := svc.GetUpdateStatus(context.Background(), "sysop-stale")
	require.NoError(t, err)
	require.Equal(t, UpdateStatusFailed, status.Status)
	require.Equal(t, "lease_expired", status.Reason)
	require.NoError(t, svc.EnsureNoActiveRollout(context.Background(), "sysop-new"))
}
