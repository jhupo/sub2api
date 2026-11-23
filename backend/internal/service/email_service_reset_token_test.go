//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type resetTokenCacheStub struct {
	emailCacheStub
	stored       *PasswordResetTokenData
	consumedHash string
	consumeErr   error
}

func (s *resetTokenCacheStub) GetPasswordResetToken(context.Context, string) (*PasswordResetTokenData, error) {
	return s.stored, nil
}

func (s *resetTokenCacheStub) SetPasswordResetToken(_ context.Context, _ string, data *PasswordResetTokenData, _ time.Duration) error {
	copy := *data
	s.stored = &copy
	return nil
}

func (s *resetTokenCacheStub) ConsumePasswordResetToken(_ context.Context, _ string, tokenHash string) (bool, error) {
	s.consumedHash = tokenHash
	if s.consumeErr != nil {
		return false, s.consumeErr
	}
	if s.stored == nil {
		return false, nil
	}
	expected := s.stored.TokenHash
	if expected == "" {
		expected = s.stored.Token
	}
	if expected != tokenHash {
		return false, nil
	}
	s.stored = nil
	return true, nil
}

func TestConsumePasswordResetToken_ComparesHashNotPlaintext(t *testing.T) {
	token := "deadbeef"
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	require.Equal(t, hash, hashPasswordResetToken(token))
	require.NotEqual(t, token, hashPasswordResetToken(token))

	cache := &resetTokenCacheStub{stored: &PasswordResetTokenData{TokenHash: hash}}
	svc := NewEmailService(nil, cache)

	require.NoError(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", token))
	require.Equal(t, hash, cache.consumedHash)
	require.ErrorIs(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", token), ErrInvalidResetToken)

	// A still-live pre-upgrade link is accepted once, without treating hashes as bearer tokens.
	cache.stored = &PasswordResetTokenData{Token: token}
	require.NoError(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", token))
	require.ErrorIs(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", token), ErrInvalidResetToken)
}

func TestPasswordResetTokenHashCannotBeUsedAsBearerToken(t *testing.T) {
	cache := &resetTokenCacheStub{stored: &PasswordResetTokenData{
		Token: "old-token", TokenHash: hashPasswordResetToken("new-token"),
	}}
	svc := NewEmailService(nil, cache)
	for _, token := range []string{"", "old-token", cache.stored.TokenHash} {
		require.ErrorIs(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", token), ErrInvalidResetToken)
		require.NotNil(t, cache.stored)
	}
	require.NoError(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", "new-token"))
}

func TestPasswordResetTokenConsumptionFailsClosedOnCacheError(t *testing.T) {
	cache := &resetTokenCacheStub{
		stored:     &PasswordResetTokenData{TokenHash: hashPasswordResetToken("token")},
		consumeErr: errors.New("redis unavailable"),
	}
	svc := NewEmailService(nil, cache)
	require.ErrorIs(t, svc.ConsumePasswordResetToken(context.Background(), "a@b.c", "token"), ErrInvalidResetToken)
	require.NotNil(t, cache.stored)
}

func TestPasswordResetEmailAlwaysStoresFreshHash(t *testing.T) {
	cache := &resetTokenCacheStub{stored: &PasswordResetTokenData{Token: "legacy-token"}}
	svc := NewEmailService(&settingRepoStub{}, cache)
	// Sending is unconfigured; inspect the value saved before the SMTP step.
	require.Error(t, svc.SendPasswordResetEmail(context.Background(), "a@b.c", "test", "https://example.com/reset"))
	require.Empty(t, cache.stored.Token)
	firstHash := cache.stored.TokenHash
	require.Len(t, firstHash, sha256.Size*2)
	require.Error(t, svc.SendPasswordResetEmail(context.Background(), "a@b.c", "test", "https://example.com/reset"))
	require.Empty(t, cache.stored.Token)
	require.NotEqual(t, firstHash, cache.stored.TokenHash)
}
