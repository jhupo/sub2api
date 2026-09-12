package service

import (
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateAccountGroupSetRequiresConfirmedPrivacy(t *testing.T) {
	group := &Group{ID: 30, Name: "private", Platform: PlatformOpenAI, RequirePrivacySet: true}
	account := &Account{ID: 4655, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	err := validateAccountGroupSet(account, []*Group{group})
	require.Equal(t, "ACCOUNT_GROUP_PRIVACY_NOT_SET", infraerrors.Reason(err))

	account.Extra = map[string]any{"privacy_mode": PrivacyModeTrainingOff}
	require.NoError(t, validateAccountGroupSet(account, []*Group{group}))
}

func TestValidateAccountGroupSetAllowsUnsetPrivacyWhenGroupDoesNotRequireIt(t *testing.T) {
	group := &Group{ID: 31, Name: "general", Platform: PlatformOpenAI}
	account := &Account{ID: 4655, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	require.NoError(t, validateAccountGroupSet(account, []*Group{group}))
}
