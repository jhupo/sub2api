package service

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const subscriptionDayDuration = 24 * time.Hour

type UserSubscription struct {
	ID            int64
	UserID        int64
	PlanID        int64
	PlanVersionID int64

	StartsAt  time.Time
	ExpiresAt time.Time
	Status    string

	DailyWindowStart   *time.Time
	WeeklyWindowStart  *time.Time
	MonthlyWindowStart *time.Time

	DailyUsageUSD      float64
	WeeklyUsageUSD     float64
	MonthlyUsageUSD    float64
	DailyReservedUSD   float64
	WeeklyReservedUSD  float64
	MonthlyReservedUSD float64

	AssignedBy *int64
	AssignedAt time.Time
	Notes      string

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time

	User           *User
	Plan           *SubscriptionPlan
	AssignedByUser *User
}

func (s *UserSubscription) IsActive() bool {
	now := time.Now()
	return s.Status == SubscriptionStatusActive && !now.Before(s.StartsAt) && now.Before(s.ExpiresAt)
}

func (s *UserSubscription) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

func (s *UserSubscription) DaysRemaining() int {
	return s.daysRemainingAt(time.Now())
}

func (s *UserSubscription) daysRemainingAt(now time.Time) int {
	remaining := s.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}

	days := int(remaining / subscriptionDayDuration)
	if remaining%subscriptionDayDuration != 0 {
		days++
	}
	return days
}

func (s *UserSubscription) IsWindowActivated() bool {
	return s.DailyWindowStart != nil && s.WeeklyWindowStart != nil && s.MonthlyWindowStart != nil
}

type SubscriptionWindowTransition struct {
	DailyStart   time.Time
	WeeklyStart  time.Time
	MonthlyStart time.Time
	ResetDaily   bool
	ResetWeekly  bool
	ResetMonthly bool
}

// AllowanceWindowTransitionAt calculates the only legal window movement before
// a new reservation. A window with outstanding reservations cannot advance;
// those requests must settle against the window in which they were authorized.
func (s *UserSubscription) AllowanceWindowTransitionAt(now time.Time) (SubscriptionWindowTransition, error) {
	transition := SubscriptionWindowTransition{}
	if s == nil {
		return transition, ErrSubscriptionNotFound
	}
	if s.DailyWindowStart == nil {
		transition.DailyStart = timezone.StartOfDay(now)
		transition.ResetDaily = true
	} else {
		transition.DailyStart = *s.DailyWindowStart
		if start, ok := s.automaticDailyWindowStartAt(now); ok {
			if s.DailyReservedUSD != 0 {
				return transition, ErrSubscriptionWindowBusy
			}
			transition.DailyStart, transition.ResetDaily = start, true
		}
	}
	if s.WeeklyWindowStart == nil {
		transition.WeeklyStart = now
		transition.ResetWeekly = true
	} else {
		transition.WeeklyStart = *s.WeeklyWindowStart
		if start, ok := s.automaticWindowStartAt(s.WeeklyWindowStart, 7*24*time.Hour, now); ok {
			if s.WeeklyReservedUSD != 0 {
				return transition, ErrSubscriptionWindowBusy
			}
			transition.WeeklyStart, transition.ResetWeekly = start, true
		}
	}
	if s.MonthlyWindowStart == nil {
		transition.MonthlyStart = now
		transition.ResetMonthly = true
	} else {
		transition.MonthlyStart = *s.MonthlyWindowStart
		if start, ok := s.automaticWindowStartAt(s.MonthlyWindowStart, 30*24*time.Hour, now); ok {
			if s.MonthlyReservedUSD != 0 {
				return transition, ErrSubscriptionWindowBusy
			}
			transition.MonthlyStart, transition.ResetMonthly = start, true
		}
	}
	return transition, nil
}

func (s *UserSubscription) HasOneTimeDailyQuota() bool {
	if s == nil || s.StartsAt.IsZero() || s.ExpiresAt.IsZero() {
		return false
	}
	return !s.ExpiresAt.After(s.StartsAt.AddDate(0, 0, 1))
}

func (s *UserSubscription) NeedsDailyReset() bool {
	return s.NeedsDailyResetAt(time.Now())
}

func (s *UserSubscription) NeedsDailyResetAt(now time.Time) bool {
	_, ok := s.automaticDailyWindowStartAt(now)
	return ok
}

func (s *UserSubscription) NeedsWeeklyReset() bool {
	return s.NeedsWeeklyResetAt(time.Now())
}

func (s *UserSubscription) NeedsWeeklyResetAt(now time.Time) bool {
	if s.WeeklyWindowStart == nil {
		return false
	}
	return !now.Before(s.WeeklyWindowStart.Add(7 * 24 * time.Hour))
}

func (s *UserSubscription) NeedsMonthlyReset() bool {
	return s.NeedsMonthlyResetAt(time.Now())
}

func (s *UserSubscription) NeedsMonthlyResetAt(now time.Time) bool {
	if s.MonthlyWindowStart == nil {
		return false
	}
	return !now.Before(s.MonthlyWindowStart.Add(30 * 24 * time.Hour))
}

// automaticDailyWindowStartAt 计算日窗口按“配置时区日历日”对齐后的当前窗口起点。
// 日额度固定在每天 0 点刷新（与周/月的期限对齐滚动窗口语义不同），因此只要持久化
// 的窗口起点落在更早的日历日，就允许推进到今天 0 点。手动重置、激活等写入的任何
// 非 0 点锚点都会在下一个 0 点被拉回日历日边界，不会永久漂移刷新时刻。
func (s *UserSubscription) automaticDailyWindowStartAt(now time.Time) (time.Time, bool) {
	if s.DailyWindowStart == nil {
		return time.Time{}, false
	}
	if s.HasOneTimeDailyQuota() {
		return time.Time{}, false
	}
	today := timezone.StartOfDay(now)
	if !today.After(timezone.StartOfDay(*s.DailyWindowStart)) {
		return time.Time{}, false
	}
	return today, true
}

// windowResetAnchor 返回周/月窗口实际推进所依据的锚点。
// 早期订阅把首个窗口初始化在开通日零点；只有这个初始值是无歧义的，之后出现的
// 零点锚点可能来自手动重置，必须保持权威。
// 自动推进（automaticWindowStartAt）与对外展示的重置时间（WeeklyResetTime/
// MonthlyResetTime）必须共用这一修正，否则仪表盘显示的重置时间会早于窗口实际
// 滚动的时间。
// 日窗口按日历日对齐（automaticDailyWindowStartAt），不走这里。
func (s *UserSubscription) windowResetAnchor(previous time.Time) time.Time {
	legacyAnchor := timezone.StartOfDay(s.StartsAt)
	if legacyAnchor.Before(s.StartsAt) && previous.Equal(legacyAnchor) {
		return s.StartsAt
	}
	return previous
}

// automaticWindowStartAt 计算周/月窗口（期限对齐滚动窗口）的当前窗口起点。
// 窗口从锚点按整数个 period 步进，且不越过订阅到期时间，避免最后一个不完整
// 周期重复发放额度（issue #5051）。日窗口不走此函数，见 automaticDailyWindowStartAt。
func (s *UserSubscription) automaticWindowStartAt(previous *time.Time, period time.Duration, now time.Time) (time.Time, bool) {
	if previous == nil {
		return time.Time{}, false
	}

	anchor := s.windowResetAnchor(*previous)
	next := anchor.Add(period)
	if now.Before(next) || !next.Before(s.ExpiresAt) {
		return time.Time{}, false
	}

	periods := now.Sub(anchor) / period
	lastPeriodBeforeExpiry := (s.ExpiresAt.Sub(anchor) - 1) / period
	if periods > lastPeriodBeforeExpiry {
		periods = lastPeriodBeforeExpiry
	}
	return anchor.Add(periods * period), true
}

func (s *UserSubscription) DailyResetTime() *time.Time {
	if s.DailyWindowStart == nil {
		return nil
	}
	if s.HasOneTimeDailyQuota() {
		t := s.ExpiresAt
		return &t
	}
	// 日窗口按日历日对齐：下次刷新固定在窗口起点所在日的次日 0 点。
	t := timezone.StartOfDay(*s.DailyWindowStart).AddDate(0, 0, 1)
	return &t
}

func (s *UserSubscription) WeeklyResetTime() *time.Time {
	if s.WeeklyWindowStart == nil {
		return nil
	}
	t := s.windowResetAnchor(*s.WeeklyWindowStart).Add(7 * 24 * time.Hour)
	return &t
}

func (s *UserSubscription) MonthlyResetTime() *time.Time {
	if s.MonthlyWindowStart == nil {
		return nil
	}
	t := s.windowResetAnchor(*s.MonthlyWindowStart).Add(30 * 24 * time.Hour)
	return &t
}

func (s *UserSubscription) CheckDailyLimit(additionalCost float64) bool {
	if s.Plan == nil || s.Plan.DailyLimitUSD == nil {
		return true
	}
	return s.DailyUsageUSD+s.DailyReservedUSD+additionalCost <= *s.Plan.DailyLimitUSD
}

func (s *UserSubscription) CheckWeeklyLimit(additionalCost float64) bool {
	if s.Plan == nil || s.Plan.WeeklyLimitUSD == nil {
		return true
	}
	return s.WeeklyUsageUSD+s.WeeklyReservedUSD+additionalCost <= *s.Plan.WeeklyLimitUSD
}

func (s *UserSubscription) CheckMonthlyLimit(additionalCost float64) bool {
	if s.Plan == nil || s.Plan.MonthlyLimitUSD == nil {
		return true
	}
	return s.MonthlyUsageUSD+s.MonthlyReservedUSD+additionalCost <= *s.Plan.MonthlyLimitUSD
}

func (s *UserSubscription) CheckAllLimits(additionalCost float64) (daily, weekly, monthly bool) {
	daily = s.CheckDailyLimit(additionalCost)
	weekly = s.CheckWeeklyLimit(additionalCost)
	monthly = s.CheckMonthlyLimit(additionalCost)
	return
}
