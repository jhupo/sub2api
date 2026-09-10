package service

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRuntimeRecoveryRequiresObservedPersistence(t *testing.T) {
	svc := &OpenAIGatewayService{}
	now := time.Now()
	account := &Account{ID: 71, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, UpdatedAt: now}
	until := now.Add(time.Hour)
	svc.BlockAccountScheduling(account, until, "429")

	// An unrelated update or a failed cooldown write cannot prove recovery.
	account.UpdatedAt = now.Add(time.Second)
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
	account.RateLimitResetAt = &until
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
	account.RateLimitResetAt = nil
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
	account.UpdatedAt = now.Add(2 * time.Second)
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
}

func TestOpenAIRuntimeRecoveryDoesNotRevokeNewBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	now := time.Now()
	account := &Account{ID: 72, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, UpdatedAt: now}
	until := now.Add(time.Hour)
	svc.BlockAccountScheduling(account, until, "429")
	account.UpdatedAt = now.Add(time.Second)
	account.RateLimitResetAt = &until
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
	svc.BlockAccountScheduling(account, until.Add(time.Hour), "429")
	account.UpdatedAt = now.Add(2 * time.Second)
	account.RateLimitResetAt = nil
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
}

func TestOpenAIRuntimeRecoveryIgnoresUnrelatedStatusChanges(t *testing.T) {
	svc := &OpenAIGatewayService{}
	now := time.Now()
	account := &Account{ID: 74, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, UpdatedAt: now}
	svc.BlockAccountScheduling(account, now.Add(time.Hour), "quota")
	account.Schedulable = false
	account.UpdatedAt = now.Add(time.Second)
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
	account.Schedulable = true
	account.UpdatedAt = now.Add(2 * time.Second)
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
}

func TestOpenAIRuntimeRecoveryMatchesDatabaseTimestampPrecision(t *testing.T) {
	svc := &OpenAIGatewayService{}
	now := time.Now().Truncate(time.Microsecond)
	account := &Account{ID: 75, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, UpdatedAt: now}
	until := now.Add(time.Hour + 123*time.Nanosecond)
	svc.BlockAccountScheduling(account, until, "quota")
	persisted := until.Truncate(time.Microsecond)
	account.UpdatedAt = now.Add(time.Second)
	account.RateLimitResetAt = &persisted
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
	account.UpdatedAt = now.Add(2 * time.Second)
	account.RateLimitResetAt = nil
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol"))
}

func TestOpenAIRuntimeRecoveryConcurrentSnapshots(t *testing.T) {
	svc := &OpenAIGatewayService{}
	now := time.Now()
	base := Account{ID: 73, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, UpdatedAt: now}
	until := now.Add(time.Hour)
	svc.BlockAccountScheduling(&base, until, "429")
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				svc.isOpenAIAccountRequestRuntimeBlocked(&base, "gpt-5.6-sol")
			}
		}()
	}
	wg.Wait()
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(&base))
}

func TestOpenAIRuntimeRecoveryConcurrentAccountIsolation(t *testing.T) {
	svc := &OpenAIGatewayService{}
	now := time.Now()
	accounts := make([]Account, 50)
	for i := range accounts {
		accounts[i] = Account{ID: int64(i + 1), Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, UpdatedAt: now}
		if i%2 == 0 {
			svc.BlockAccountScheduling(&accounts[i], now.Add(time.Hour), "quota")
		}
	}
	var wg sync.WaitGroup
	for user := 0; user < 30; user++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 20; round++ {
				for i := range accounts {
					if got := svc.isOpenAIAccountRequestRuntimeBlocked(&accounts[i], "gpt-5.6-sol"); got != (i%2 == 0) {
						t.Errorf("account %d inherited another account's block: %v", i+1, got)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkOpenAIRuntimeSnapshotCheck(b *testing.B) {
	svc := &OpenAIGatewayService{}
	now := time.Now()
	account := &Account{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, UpdatedAt: now}
	svc.BlockAccountScheduling(account, now.Add(time.Hour), "quota")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6-sol")
		}
	})
}
