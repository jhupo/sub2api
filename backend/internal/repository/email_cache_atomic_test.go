package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newMiniredisEmailCache(t *testing.T) (service.EmailCache, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewEmailCache(rdb), mr, rdb
}

func TestEmailCache_ConcurrentWrongCodesCannotExceedAttemptCap(t *testing.T) {
	cache, _, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "user@example.com"

	svc := service.NewEmailService(nil, cache)
	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{
		Code:      "123456",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}, 15*time.Minute))

	const workers = 50
	var invalid, maxed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.VerifyCode(ctx, email, "000000")
			switch {
			case errors.Is(err, service.ErrInvalidVerifyCode):
				invalid.Add(1)
			case errors.Is(err, service.ErrVerifyCodeMaxAttempts):
				maxed.Add(1)
			default:
				t.Errorf("unexpected result: %v", err)
			}
		}()
	}
	wg.Wait()

	// Only attempts 1..4 may return "invalid"; every other guess is rejected by the cap.
	require.LessOrEqual(t, int(invalid.Load()), 4)
	require.Equal(t, workers, int(invalid.Load()+maxed.Load()))

	// Even the correct code is now rejected.
	require.ErrorIs(t, svc.VerifyCode(ctx, email, "123456"), service.ErrVerifyCodeMaxAttempts)

	data, err := cache.GetVerificationCode(ctx, email)
	require.NoError(t, err)
	require.GreaterOrEqual(t, data.Attempts, 5)
}

func TestEmailCache_AttemptsResetOnNewCodeAndTTLFollowsCode(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "User@Example.com"

	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "1"}, time.Minute))
	n, err := cache.IncrVerificationCodeAttempts(ctx, email)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Greater(t, mr.TTL(verifyCodeKey(email)+attemptsKeySuffix), time.Duration(0))

	require.NoError(t, cache.SetVerificationCode(ctx, email, &service.VerificationCodeData{Code: "2"}, time.Minute))
	data, err := cache.GetVerificationCode(ctx, email)
	require.NoError(t, err)
	require.Equal(t, 0, data.Attempts)

	require.NoError(t, cache.DeleteVerificationCode(ctx, email))
	_, err = cache.IncrVerificationCodeAttempts(ctx, email)
	require.Error(t, err)
	require.False(t, mr.Exists(verifyCodeKey(email)+attemptsKeySuffix))
}

func TestEmailCache_PasswordResetTokenHashedAndSingleUse(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "reset@example.com"

	svc := service.NewEmailService(nil, cache)

	// Seed the token the same way SendPasswordResetEmail does (hash only).
	token, err := svc.GeneratePasswordResetToken()
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(token))
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{
		TokenHash: hex.EncodeToString(sum[:]), CreatedAt: time.Now(),
	}, 30*time.Minute))

	raw, err := mr.Get(passwordResetKey(email))
	require.NoError(t, err)
	require.False(t, strings.Contains(raw, token), "plaintext token must not be stored")

	require.NoError(t, svc.VerifyPasswordResetToken(ctx, email, token))
	require.ErrorIs(t, svc.VerifyPasswordResetToken(ctx, email, hex.EncodeToString(sum[:])), service.ErrInvalidResetToken)
	require.ErrorIs(t, svc.ConsumePasswordResetToken(ctx, email, "wrong"), service.ErrInvalidResetToken)

	const workers = 30
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if svc.ConsumePasswordResetToken(ctx, email, token) == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), ok.Load())
	require.False(t, mr.Exists(passwordResetKey(email)))
}

func TestEmailCache_ConsumePasswordResetTokenMismatchKeepsToken(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "keep@example.com"
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{Token: "abc"}, time.Minute))

	ok, err := cache.ConsumePasswordResetToken(ctx, email, "xyz")
	require.NoError(t, err)
	require.False(t, ok)
	require.True(t, mr.Exists(passwordResetKey(email)))

	ok, err = cache.ConsumePasswordResetToken(ctx, email, "abc")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.ConsumePasswordResetToken(ctx, email, "abc")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestEmailCache_LegacyAttemptCountAndTTLRemainBounded(t *testing.T) {
	cache, mr, rdb := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "legacy@example.com"
	for _, notify := range []bool{false, true} {
		key, increment := verifyCodeKey(email), cache.IncrVerificationCodeAttempts
		if notify {
			key, increment = notifyVerifyKey(email), cache.IncrNotifyVerifyCodeAttempts
		}
		// Simulate the single JSON value written by the pre-upgrade cache.
		raw, err := json.Marshal(service.VerificationCodeData{Code: "123456", Attempts: 3})
		require.NoError(t, err)
		require.NoError(t, rdb.Set(ctx, key, raw, time.Minute).Err())
		mr.FastForward(10 * time.Second)
		n, err := increment(ctx, email)
		require.NoError(t, err)
		require.Equal(t, 4, n)
		require.Equal(t, mr.TTL(key), mr.TTL(key+attemptsKeySuffix))
		mr.FastForward(5 * time.Second)
		n, err = increment(ctx, email)
		require.NoError(t, err)
		require.Equal(t, 5, n)
		require.Equal(t, mr.TTL(key), mr.TTL(key+attemptsKeySuffix))
	}
}

func TestEmailCache_LegacyResetLinksConsumeOnceWithoutExtendingTTL(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email, token := "legacy-reset@example.com", "legacy-plaintext-token"
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{Token: token}, time.Minute))
	mr.FastForward(20 * time.Second)
	svc := service.NewEmailService(nil, cache)
	require.NoError(t, svc.VerifyPasswordResetToken(ctx, email, token))
	require.Equal(t, 40*time.Second, mr.TTL(passwordResetKey(email)))
	require.NoError(t, svc.ConsumePasswordResetToken(ctx, email, token))
	require.ErrorIs(t, svc.ConsumePasswordResetToken(ctx, email, token), service.ErrInvalidResetToken)
}

func TestEmailCache_ResetConsumeDoesNotDeleteReplacement(t *testing.T) {
	cache, mr, _ := newMiniredisEmailCache(t)
	ctx := context.Background()
	email := "replacement@example.com"
	require.NoError(t, cache.SetPasswordResetToken(ctx, email, &service.PasswordResetTokenData{TokenHash: "new-hash"}, time.Minute))
	ok, err := cache.ConsumePasswordResetToken(ctx, email, "old-hash")
	require.NoError(t, err)
	require.False(t, ok)
	require.True(t, mr.Exists(passwordResetKey(email)))
}
