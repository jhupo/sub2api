package domain

import "time"

type PlanPriceStep struct {
	At    time.Time `json:"at"`
	Price float64   `json:"price"`
}

type PlanSaleWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Sale rules affect new orders only; issued entitlements retain their terms.
type PlanSaleSchedule struct {
	EndsAt       *time.Time       `json:"ends_at"`
	PriceSteps   []PlanPriceStep  `json:"price_steps"`
	DailyWindows []PlanSaleWindow `json:"daily_windows"`
}
