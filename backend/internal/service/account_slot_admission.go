package service

import (
	"context"
	"errors"
	"time"
)

// AccountSlotAdmission is resolved for each acquisition, including queued
// retries. Pressure is evaluated atomically with the slot count by the store.
type AccountSlotAdmission struct {
	MaxConcurrency int
	PressureModel  string
	PressureWindow time.Duration
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
