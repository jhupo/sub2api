package service

import (
	"context"
	"errors"
	"time"
)

// AccountSlotAdmission is resolved for each acquisition, including queued
// retries. Pressure is evaluated atomically with the slot count by the store.
type AccountSlotAdmission struct {
	MaxConcurrency   int
	PressureModel    string
	PressureWindow   time.Duration
	SoftLimitPercent int
}

type accountSoftAdmissionKey struct{}

// AccountSoftConcurrencyLimit reserves headroom without stranding small pools.
// Zero disables the soft pass; it never changes the configured hard limit.
func AccountSoftConcurrencyLimit(limit, percent int) int {
	if limit <= 0 || percent <= 0 || percent >= 100 {
		return limit
	}
	return limit/100*percent + (limit%100*percent+99)/100
}

type adaptiveAccountSlotCache interface {
	AcquireAdaptiveAccountSlot(context.Context, int64, AccountSlotAdmission, string) (bool, error)
}

type accountSlotAdmissionKey struct{}
type accountSlotAdmissionResolver func(context.Context, int64) (*AccountSlotAdmission, error)

var errAdaptiveAdmissionUnavailable = errors.New("adaptive account admission unavailable")

func resolveAccountSlotAdmission(ctx context.Context, accountID int64, configured int) (*AccountSlotAdmission, error) {
	if resolve, ok := ctx.Value(accountSlotAdmissionKey{}).(accountSlotAdmissionResolver); ok {
		policy, err := resolve(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if policy != nil {
			return policy, nil
		}
	}
	return &AccountSlotAdmission{MaxConcurrency: configured}, nil
}
