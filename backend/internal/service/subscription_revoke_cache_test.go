//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type revokeCacheUserSubRepoStub struct {
	userSubRepoNoop

	sub     *UserSubscription
	deleted bool
}

func (r *revokeCacheUserSubRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id || r.deleted {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func (r *revokeCacheUserSubRepoStub) Delete(_ context.Context, id int64) error {
	if r.sub == nil || r.sub.ID != id || r.deleted {
		return ErrSubscriptionNotFound
	}
	r.deleted = true
	return nil
}

func TestRevokeSubscription_DeletesEntitlementSynchronously(t *testing.T) {
	repo := &revokeCacheUserSubRepoStub{
		sub: &UserSubscription{
			ID:            1,
			UserID:        10,
			PlanID:        20,
			PlanVersionID: 21,
			Status:        SubscriptionStatusActive,
			ExpiresAt:     time.Now().Add(time.Hour),
		},
	}
	svc := NewSubscriptionService(repo, nil)

	err := svc.RevokeSubscription(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, repo.deleted)
}

type restoreUserSubRepoStub struct {
	userSubRepoNoop

	sub            *UserSubscription
	existsActive   bool
	restoreCalls   int
	restoredStatus string
}

func (r *restoreUserSubRepoStub) GetByIDIncludeDeleted(_ context.Context, id int64) (*UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func (r *restoreUserSubRepoStub) ExistsActiveByUserIDAndPlanVersionID(context.Context, int64, int64) (bool, error) {
	return r.existsActive, nil
}

func (r *restoreUserSubRepoStub) Restore(_ context.Context, id int64, restoredStatus string) (*UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	r.restoreCalls++
	r.restoredStatus = restoredStatus
	cp := *r.sub
	cp.Status = restoredStatus
	cp.DeletedAt = nil
	r.sub = &cp
	return &cp, nil
}

func TestRestoreSubscription_ExpiredActiveRestoresAsExpired(t *testing.T) {
	deletedAt := time.Now().Add(-time.Hour)
	repo := &restoreUserSubRepoStub{
		sub: &UserSubscription{
			ID:            1,
			UserID:        10,
			PlanID:        20,
			PlanVersionID: 21,
			Status:        SubscriptionStatusActive,
			ExpiresAt:     time.Now().Add(-time.Minute),
			DeletedAt:     &deletedAt,
		},
	}
	svc := NewSubscriptionService(repo, nil)

	restored, err := svc.RestoreSubscription(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, repo.restoreCalls)
	require.Equal(t, SubscriptionStatusExpired, repo.restoredStatus)
	require.Equal(t, SubscriptionStatusExpired, restored.Status)
	require.Nil(t, restored.DeletedAt)
}

func TestRestoreSubscription_NotRevokedReturnsConflict(t *testing.T) {
	repo := &restoreUserSubRepoStub{
		sub: &UserSubscription{
			ID:            1,
			UserID:        10,
			PlanID:        20,
			PlanVersionID: 21,
			Status:        SubscriptionStatusActive,
			ExpiresAt:     time.Now().Add(time.Hour),
		},
	}
	svc := NewSubscriptionService(repo, nil)

	_, err := svc.RestoreSubscription(context.Background(), 1)
	require.ErrorIs(t, err, ErrSubscriptionNotRevoked)
	require.Zero(t, repo.restoreCalls)
}

func TestRestoreSubscription_LiveSubscriptionConflict(t *testing.T) {
	deletedAt := time.Now().Add(-time.Hour)
	repo := &restoreUserSubRepoStub{
		existsActive: true,
		sub: &UserSubscription{
			ID:            1,
			UserID:        10,
			PlanID:        20,
			PlanVersionID: 21,
			Status:        SubscriptionStatusExpired,
			ExpiresAt:     time.Now().Add(-time.Hour),
			DeletedAt:     &deletedAt,
		},
	}
	svc := NewSubscriptionService(repo, nil)

	_, err := svc.RestoreSubscription(context.Background(), 1)
	require.ErrorIs(t, err, ErrSubscriptionRestoreConflict)
	require.Zero(t, repo.restoreCalls)
}
