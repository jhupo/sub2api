package service

import (
	"context"
)

// AccountSlotAdmission is resolved for each acquisition, including queued
// retries.
type AccountSlotAdmission struct {
	MaxConcurrency   int
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

func resolveAccountSlotAdmission(ctx context.Context, accountID int64, configured int) (*AccountSlotAdmission, error) {
	return &AccountSlotAdmission{MaxConcurrency: configured}, nil
}
