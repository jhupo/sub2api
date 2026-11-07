package service

import (
	"math"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

func saleMinute(raw string) (int, bool) {
	t, err := time.Parse("15:04", raw)
	return t.Hour()*60 + t.Minute(), err == nil && len(raw) == 5
}

func validateSaleSchedule(mode string, deadline *time.Time, schedule *domain.PlanSaleSchedule) error {
	if schedule == nil {
		return nil
	}
	invalid := func(message string) error { return infraerrors.BadRequest("PLAN_SALE_INVALID", message) }
	if mode == "" && (schedule.EndsAt != nil || len(schedule.PriceSteps) > 0 || len(schedule.DailyWindows) > 0) {
		return invalid("sale schedules require a quota card")
	}
	if len(schedule.PriceSteps) > 32 || len(schedule.DailyWindows) > 16 {
		return invalid("too many sale schedule entries")
	}
	end := schedule.EndsAt
	if mode == "deadline" && deadline != nil {
		if end != nil && end.After(*deadline) {
			return invalid("sale end must not be later than the card deadline")
		}
		if end == nil {
			end = deadline
		}
	}
	sort.Slice(schedule.PriceSteps, func(i, j int) bool { return schedule.PriceSteps[i].At.Before(schedule.PriceSteps[j].At) })
	for i, step := range schedule.PriceSteps {
		if step.At.IsZero() || step.Price <= 0 || math.IsNaN(step.Price) || math.IsInf(step.Price, 0) {
			return invalid("price steps require a timestamp and a positive finite price")
		}
		if (i > 0 && step.At.Equal(schedule.PriceSteps[i-1].At)) || (end != nil && !step.At.Before(*end)) {
			return invalid("price step times must be unique and before the sale end")
		}
	}
	if len(schedule.DailyWindows) > 0 && mode != "hours" {
		return invalid("daily sale windows require an hourly card")
	}
	var occupied [1440]bool
	for _, window := range schedule.DailyWindows {
		start, okStart := saleMinute(window.Start)
		end, okEnd := saleMinute(window.End)
		if !okStart || !okEnd || start == end {
			return invalid("daily windows require distinct HH:MM start and end times")
		}
		for minute := start; minute != end; minute = (minute + 1) % 1440 {
			if occupied[minute] {
				return invalid("daily sale windows must not overlap")
			}
			occupied[minute] = true
		}
	}
	return nil
}

// ApplySaleAt resolves catalog availability and price without changing purchased terms.
func (p *SubscriptionPlan) ApplySaleAt(now time.Time) {
	p.SaleTimezone = timezone.Location().String()
	p.SaleAvailable = p.ForSale && !p.IsHistorical
	p.SaleEndsAt = p.SaleSchedule.EndsAt
	if p.CardMode == "deadline" && p.DeadlineAt != nil && (p.SaleEndsAt == nil || p.DeadlineAt.Before(*p.SaleEndsAt)) {
		p.SaleEndsAt = p.DeadlineAt
	}
	if p.SaleEndsAt != nil && !now.Before(*p.SaleEndsAt) {
		p.SaleAvailable = false
	}
	p.CurrentPrice = p.Price
	var appliedAt time.Time
	for _, step := range p.SaleSchedule.PriceSteps {
		if !now.Before(step.At) && (appliedAt.IsZero() || step.At.After(appliedAt)) {
			p.CurrentPrice, appliedAt = step.Price, step.At
		}
	}
	if len(p.SaleSchedule.DailyWindows) == 0 {
		return
	}
	local := now.In(timezone.Location())
	minute := local.Hour()*60 + local.Minute()
	// Adjacent windows form a single continuous sale period.
	var occupied [1440]bool
	for _, window := range p.SaleSchedule.DailyWindows {
		start, a := saleMinute(window.Start)
		end, b := saleMinute(window.End)
		if !a || !b || start == end {
			continue
		}
		for m := start; m != end; m = (m + 1) % 1440 {
			occupied[m] = true
		}
	}
	active := occupied[minute]
	p.SaleAvailable = p.SaleAvailable && active
	if active {
		for offset := 1; offset <= 1440; offset++ {
			if !occupied[(minute+offset)%1440] {
				boundary := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute()+offset, 0, 0, local.Location())
				if p.SaleEndsAt == nil || boundary.Before(*p.SaleEndsAt) {
					p.SaleEndsAt = &boundary
				}
				break
			}
		}
	}
}
