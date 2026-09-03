package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type schedulerFreshnessRepoStub struct {
	AccountRepository

	mu              sync.Mutex
	projection      map[int64]SchedulerFreshness
	projectionErr   error
	projectionCalls int
	fallbackCalls   int
	projectionIDs   [][]int64
}

func (r *schedulerFreshnessRepoStub) ReadSchedulerFreshness(_ context.Context, ids []int64) (map[int64]SchedulerFreshness, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projectionCalls++
	r.projectionIDs = append(r.projectionIDs, append([]int64(nil), ids...))
	if r.projectionErr != nil {
		return nil, r.projectionErr
	}
	values := make(map[int64]SchedulerFreshness, len(r.projection))
	for id, value := range r.projection {
		value.GroupIDs = append([]int64(nil), value.GroupIDs...)
		values[id] = value
	}
	return values, nil
}

func (r *schedulerFreshnessRepoStub) GetByIDs(_ context.Context, _ []int64) ([]*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallbackCalls++
	return nil, errors.New("unexpected full account fallback")
}

func (r *schedulerFreshnessRepoStub) setProjection(values map[int64]SchedulerFreshness, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projection = values
	r.projectionErr = err
}

func schedulerFreshnessTestValue(id int64, parentID *int64) SchedulerFreshness {
	return SchedulerFreshness{
		ID:              id,
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		Status:          StatusActive,
		Schedulable:     true,
		ParentAccountID: parentID,
	}
}

func TestSchedulerFreshnessPrimesCandidatesAndSharedParentInOneBatch(t *testing.T) {
	parentID := int64(91)
	repo := &schedulerFreshnessRepoStub{projection: map[int64]SchedulerFreshness{
		11: schedulerFreshnessTestValue(11, &parentID),
		12: schedulerFreshnessTestValue(12, &parentID),
		91: schedulerFreshnessTestValue(91, nil),
	}}
	accounts := []Account{
		{ID: 11, ParentAccountID: &parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 12, ParentAccountID: &parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	}
	snapshot := &SchedulerSnapshotService{}
	ctx := withSchedulerFreshness(context.Background(), repo, snapshot)
	ctx = withSchedulerFreshnessAccounts(ctx, repo, snapshot, accounts)

	require.Len(t, applySchedulerFreshnessAccounts(ctx, accounts), 2)
	parent, known := schedulerFreshnessLookupResult(ctx, parentID)
	require.True(t, known)
	require.NotNil(t, parent)
	require.True(t, parent.IsOpenAIOAuth())

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, 1, repo.projectionCalls)
	require.Zero(t, repo.fallbackCalls)
	ids := append([]int64(nil), repo.projectionIDs[0]...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	require.Equal(t, []int64{11, 12, 91}, ids)
}

func TestSchedulerFreshnessProjectionFailureFailsClosedWithoutFallback(t *testing.T) {
	repo := &schedulerFreshnessRepoStub{projectionErr: errors.New("projection unavailable")}
	accounts := []Account{{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeOAuth}}
	snapshot := &SchedulerSnapshotService{}
	ctx := withSchedulerFreshness(context.Background(), repo, snapshot)
	ctx = withSchedulerFreshnessAccounts(ctx, repo, snapshot, accounts)

	require.Empty(t, applySchedulerFreshnessAccounts(ctx, accounts))
	account, known := schedulerFreshnessLookupResult(ctx, accounts[0].ID)
	require.True(t, known)
	require.Nil(t, account)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, 1, repo.projectionCalls)
	require.Zero(t, repo.fallbackCalls)
}

func TestSchedulerFreshnessMissingAccountFailsClosed(t *testing.T) {
	repo := &schedulerFreshnessRepoStub{projection: map[int64]SchedulerFreshness{
		31: schedulerFreshnessTestValue(31, nil),
	}}
	accounts := []Account{
		{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	}
	snapshot := &SchedulerSnapshotService{}
	ctx := withSchedulerFreshness(context.Background(), repo, snapshot)
	ctx = withSchedulerFreshnessAccounts(ctx, repo, snapshot, accounts)

	got := applySchedulerFreshnessAccounts(ctx, accounts)
	require.Len(t, got, 1)
	require.Equal(t, int64(31), got[0].ID)
	missing, known := schedulerFreshnessLookupResult(ctx, 32)
	require.True(t, known)
	require.Nil(t, missing)
}

func TestRefreshSchedulerRequestContextReloadsEachTurn(t *testing.T) {
	account := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	repo := &schedulerFreshnessRepoStub{projection: map[int64]SchedulerFreshness{
		account.ID: schedulerFreshnessTestValue(account.ID, nil),
	}}
	svc := &OpenAIGatewayService{accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{}}

	ctx := svc.PrepareSchedulerRequestContext(context.Background())
	fresh, ok := svc.RefreshSchedulerAccountFreshness(ctx, account, nil, "")
	require.True(t, ok)
	require.NotNil(t, fresh)

	disabled := schedulerFreshnessTestValue(account.ID, nil)
	disabled.Schedulable = false
	repo.setProjection(map[int64]SchedulerFreshness{account.ID: disabled}, nil)
	_, cachedOK := svc.RefreshSchedulerAccountFreshness(ctx, account, nil, "")
	require.True(t, cachedOK, "one request scope must reuse its frozen projection")

	nextTurnCtx := svc.RefreshSchedulerRequestContext(ctx)
	refreshed, refreshedOK := svc.RefreshSchedulerAccountFreshness(nextTurnCtx, account, nil, "")
	require.False(t, refreshedOK)
	require.Nil(t, refreshed)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, 2, repo.projectionCalls)
}

func TestRefreshSchedulerAccountFreshnessRejectsDisabledParent(t *testing.T) {
	parentID := int64(52)
	child := &Account{
		ID:              51,
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		Status:          StatusActive,
		Schedulable:     true,
		ParentAccountID: &parentID,
	}
	parent := schedulerFreshnessTestValue(parentID, nil)
	parent.Status = StatusDisabled
	repo := &schedulerFreshnessRepoStub{projection: map[int64]SchedulerFreshness{
		child.ID: schedulerFreshnessTestValue(child.ID, &parentID),
		parentID: parent,
	}}
	svc := &OpenAIGatewayService{accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{}}
	ctx := svc.PrepareSchedulerRequestContext(context.Background())

	fresh, ok := svc.RefreshSchedulerAccountFreshness(ctx, child, nil, "")
	require.False(t, ok)
	require.Nil(t, fresh)
}

func TestRefreshSchedulerAccountFreshnessRejectsGroupRemoval(t *testing.T) {
	groupID := int64(71)
	account := &Account{
		ID:          70,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		GroupIDs:    []int64{groupID},
		AccountGroups: []AccountGroup{
			{AccountID: 70, GroupID: groupID},
		},
	}
	freshness := schedulerFreshnessTestValue(account.ID, nil)
	freshness.GroupIDs = nil
	repo := &schedulerFreshnessRepoStub{projection: map[int64]SchedulerFreshness{account.ID: freshness}}
	svc := &OpenAIGatewayService{accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{}}
	ctx := svc.PrepareSchedulerRequestContext(context.Background())

	fresh, ok := svc.RefreshSchedulerAccountFreshness(ctx, account, &groupID, "")
	require.False(t, ok)
	require.Nil(t, fresh)
}

func TestRefreshSchedulerAccountFreshnessRejectsRequiredPrivacyLoss(t *testing.T) {
	groupID := int64(81)
	account := &Account{
		ID:          80,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		GroupIDs:    []int64{groupID},
		AccountGroups: []AccountGroup{
			{AccountID: 80, GroupID: groupID},
		},
		Extra: map[string]any{"privacy_mode": PrivacyModeTrainingOff},
	}
	freshness := schedulerFreshnessTestValue(account.ID, nil)
	freshness.GroupIDs = []int64{groupID}
	repo := &schedulerFreshnessRepoStub{projection: map[int64]SchedulerFreshness{account.ID: freshness}}
	svc := &OpenAIGatewayService{accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{}}
	ctx := svc.PrepareSchedulerRequestContext(context.Background())
	ctx = context.WithValue(ctx, openAIGroupPrivacyRequirementContextKey{}, openAIGroupPrivacyRequirement{
		groupID:  groupID,
		required: true,
	})

	fresh, ok := svc.RefreshSchedulerAccountFreshness(ctx, account, &groupID, "")
	require.False(t, ok)
	require.Nil(t, fresh)
}

func TestSchedulerHydratedAccountReturnsPrivateCopies(t *testing.T) {
	ctx := withSchedulerFreshness(context.Background(), &schedulerFreshnessRepoStub{}, &SchedulerSnapshotService{})
	original := &Account{
		ID:          61,
		Credentials: map[string]any{"token": "secret"},
		Extra:       map[string]any{"nested": map[string]any{"enabled": true}},
	}
	rememberSchedulerHydratedAccount(ctx, original)
	original.Credentials["token"] = "mutated-after-store"
	original.Extra["nested"].(map[string]any)["enabled"] = false

	first, ok := schedulerHydratedAccount(ctx, original.ID)
	require.True(t, ok)
	require.Equal(t, "secret", first.Credentials["token"])
	require.Equal(t, true, first.Extra["nested"].(map[string]any)["enabled"])

	first.Credentials["token"] = "mutated-after-read"
	second, ok := schedulerHydratedAccount(ctx, original.ID)
	require.True(t, ok)
	require.Equal(t, "secret", second.Credentials["token"])
}
