package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const (
	DefaultBalancePreauthorizationOutputWindow = 256
	balancePreauthorizationCompensationTimeout = 3 * time.Second
	balancePreauthorizationWalletTimeout       = 3 * time.Second
)

// 余额不足导致预扣/流式续扣失败时映射为 403，而非 402/429，这是刻意的计费决策：
// 属于客户端无法靠重试或等待消除的确定性拒绝（额度不足即拒绝本次请求）。
//   - 不用 429：429 会诱导客户端退避后重试，放大无法结算的无效请求负载；
//   - 不用 402：402 虽不诱导退避，但语义上暗示"充值即可放行本次"，与此处的确定性拒绝
//     不符，且用 403 与上游权限类拒绝语义保持一致。
//
// 注意：该 sentinel 同时用于入口预扣(lifecycle line 243)与流式续扣(guard.go:130、
// passthrough:1787)两条路径；两处返回前均已执行 compensateAuthorizationFailure/退款，
// 不会漏扣或残留 hold——修改状态码不得改变这一补偿前置。
var (
	ErrBalanceWithholdingFailed = infraerrors.Forbidden(
		"BALANCE_WITHHOLDING_FAILED",
		"Insufficient balance, withholding failed",
	)
)

type balancePreauthorizationCostCalculator interface {
	CalculateCostUnified(input CostInput) (*CostBreakdown, error)
}

type balancePreauthorizationSnapshotReader interface {
	LoadLiveBalanceInitializationSnapshot(context.Context, int64, string, int64) (LiveBalanceInitializationSnapshot, error)
}

type balancePreauthorizationWallet interface {
	AuthorizeLiveBalance(ctx context.Context, userID int64, attemptID string, fallbackBalance, holdAmount float64) (LiveBalanceResult, error)
	TopUpLiveBalance(ctx context.Context, userID int64, attemptID string, targetHoldAmount float64) (LiveBalanceResult, error)
	FinalizeLiveBalance(ctx context.Context, userID int64, attemptID string, actualAmount float64) (LiveBalanceResult, error)
	RefundLiveBalance(ctx context.Context, userID int64, attemptID string) (LiveBalanceResult, error)
}

// balancePreauthorizationAttemptCleaner is intentionally optional. Terminal
// attempt markers are needed until PostgreSQL records the terminal transition;
// implementations that can remove them may do so afterwards.
type balancePreauthorizationAttemptCleaner interface {
	DeleteLiveBalanceAttempt(context.Context, int64, string) error
}

type balancePreauthorizationWatermarkedWallet interface {
	AuthorizeExistingLiveBalance(
		ctx context.Context,
		userID int64,
		attemptID string,
		holdAmount float64,
	) (LiveBalanceResult, error)
	AuthorizeLiveBalanceAtWatermark(
		ctx context.Context,
		userID int64,
		attemptID string,
		fallbackBalance float64,
		fallbackWatermark int64,
		holdAmount float64,
	) (LiveBalanceResult, error)
	AuthorizeLiveBalanceAtWatermarkIfSafe(
		ctx context.Context,
		userID int64,
		attemptID string,
		fallbackBalance float64,
		fallbackWatermark int64,
		holdAmount float64,
		allowInitialize bool,
	) (LiveBalanceResult, error)
	AuthorizeLiveBalanceAtSnapshotWatermarkIfSafe(
		ctx context.Context,
		userID int64,
		attemptID string,
		fallbackBalance float64,
		fallbackWatermark int64,
		holdAmount float64,
		allowInitialize bool,
	) (LiveBalanceResult, error)
	TopUpLiveBalanceAtSnapshotWatermark(context.Context, int64, string, int64, float64) (LiveBalanceResult, error)
}

type balancePreauthorizationRepository interface {
	PrepareBalancePreauthorization(ctx context.Context, cmd *BalancePreauthorizationCommand) (*BalancePreauthorizationRecord, error)
	MarkBalancePreauthorizationAuthorized(ctx context.Context, requestID string, apiKeyID int64) error
	BeginBalancePreauthorizationFinalization(ctx context.Context, requestID string, apiKeyID int64, amount float64, requestFingerprint string) error
	CompleteBalancePreauthorizationSettlement(ctx context.Context, requestID string, apiKeyID int64) error
	BeginBalancePreauthorizationRefund(ctx context.Context, requestID string, apiKeyID int64) error
	CompleteBalancePreauthorizationRefund(ctx context.Context, requestID string, apiKeyID int64) error
}

// BalancePreauthorizationService owns the durable request hold lifecycle.
// Pricing and validation happen before any mutation. Once prepare succeeds,
// every uncertain dependency result is fail-closed and left recoverable in PG.
type BalancePreauthorizationService struct {
	cfg             *config.Config
	settingService  *SettingService
	costCalculator  balancePreauthorizationCostCalculator
	snapshotReader  balancePreauthorizationSnapshotReader
	wallet          balancePreauthorizationWallet
	watermarkWallet balancePreauthorizationWatermarkedWallet
	repo            balancePreauthorizationRepository
}

func (s *BalancePreauthorizationService) cleanupLiveBalanceAttempt(ctx context.Context, userID int64, attemptID string) {
	if s == nil {
		return
	}
	cleaner, ok := s.wallet.(balancePreauthorizationAttemptCleaner)
	if !ok {
		return
	}
	_ = cleaner.DeleteLiveBalanceAttempt(ctx, userID, attemptID)
}

func NewBalancePreauthorizationService(
	cfg *config.Config,
	settingService *SettingService,
	billingService *BillingService,
	billingCacheService *BillingCacheService,
	repo UsageBillingRepository,
) *BalancePreauthorizationService {
	service := &BalancePreauthorizationService{
		cfg:            cfg,
		settingService: settingService,
		costCalculator: billingService,
		repo:           repo,
	}
	service.snapshotReader, _ = repo.(balancePreauthorizationSnapshotReader)
	if billingCacheService != nil {
		service.wallet = billingCacheService.cache
		service.watermarkWallet, _ = billingCacheService.cache.(balancePreauthorizationWatermarkedWallet)
	}
	return service
}

// PreauthorizationEstimateKind selects how estimateHold prices a request.
// The token upper-bound path is the historical default for chat/text traffic;
// the per-request path prices count/size/duration-metered endpoints (images,
// video, standalone search) directly through the unified cost engine.
type PreauthorizationEstimateKind uint8

const (
	// PreauthorizationEstimateTokenUpperBound treats BillableInputBytes as a
	// conservative token upper bound and holds the largest of the input,
	// cache-read, and cache-creation pricing scenarios plus an output window.
	PreauthorizationEstimateTokenUpperBound PreauthorizationEstimateKind = iota
	// PreauthorizationEstimatePerRequest prices the request once from explicit
	// per-request billing units (image count, size tier, video seconds) using
	// the same BillingModeImage/Video/PerRequest path as post-usage settlement.
	PreauthorizationEstimatePerRequest
	// PreauthorizationEstimateFixed reserves a trusted, already calculated
	// non-model charge such as standalone search.
	PreauthorizationEstimateFixed
)

// PerRequestPreauthorizationEstimate carries the already-parsed billing units
// for a count/size/duration-metered endpoint. All fields mirror CostInput so
// the reserved hold is priced with the exact policy used at settlement.
type PerRequestPreauthorizationEstimate struct {
	// RequestCount is the number of billable units (e.g. images requested).
	RequestCount int
	// UsageUnits is a continuous billable quantity (e.g. total video seconds).
	// When positive it takes precedence over RequestCount in the cost engine.
	UsageUnits float64
	// SizeTier is the per-request size label ("1K"/"2K"/"4K"/"HD" ...).
	SizeTier string
}

// BalancePreauthorizationRequest carries the exact pricing context frozen for
// the request. For the token upper-bound estimate kind, Tokens in CostInput are
// ignored: the service prices the same conservative byte upper bound as input,
// cache-read, and cache-creation and holds the largest result. For the
// per-request estimate kind, PerRequestEstimate supplies the billing units and
// the output window is unused.
type BalancePreauthorizationRequest struct {
	RequestID                 string
	APIKeyID                  int64
	UserID                    int64
	AuthorizationFingerprint  string
	BillingType               int8
	BillableInputBytes        int
	EstimatedInputTokens      int
	InitialOutputWindowTokens int
	DisableOutputReservation  bool
	CostInput                 CostInput
	EstimateKind              PreauthorizationEstimateKind
	PerRequestEstimate        PerRequestPreauthorizationEstimate
	FixedAmount               float64
	ExpiresAt                 time.Time
}

// BalancePreauthorizationResumeRequest identifies one exact existing hold.
// Resume never estimates, prepares a new charge, or initializes a wallet.
type BalancePreauthorizationResumeRequest struct {
	RequestID                string
	APIKeyID                 int64
	UserID                   int64
	AuthorizationFingerprint string
	HoldAmount               float64
}

// RequiresPreauthorization lets handlers avoid pricing and hashing work for
// modes that Preauthorize would immediately skip.
func (s *BalancePreauthorizationService) RequiresPreauthorization(ctx context.Context, billingType int8) bool {
	if s == nil || billingType != BillingTypeBalance {
		return false
	}
	return (s.cfg == nil || s.cfg.RunMode != config.RunModeSimple) && s.balancePreauthorizationEnabled(ctx)
}

func (s *BalancePreauthorizationService) balancePreauthorizationEnabled(ctx context.Context) bool {
	if s == nil {
		return false
	}
	if s.settingService != nil {
		return s.settingService.IsBalancePreauthorizationEnabled(ctx)
	}
	return true
}

// Preauthorize returns nil without touching billing state in simple or
// subscription mode. Balance mode returns a request-owned guard on success.
func (s *BalancePreauthorizationService) Preauthorize(
	ctx context.Context,
	request BalancePreauthorizationRequest,
) (*BalancePreauthorizationGuard, error) {
	if s == nil {
		return nil, balancePreauthorizationUnavailable(errors.New("balance preauthorization service is nil"))
	}
	if (s.cfg != nil && s.cfg.RunMode == config.RunModeSimple) || !s.balancePreauthorizationEnabled(ctx) {
		return nil, nil
	}
	if request.BillingType == BillingTypeSubscription {
		return nil, nil
	}
	if err := validateBalancePreauthorizationRequest(&request); err != nil {
		return nil, balancePreauthorizationUnavailable(err)
	}
	if s.costCalculator == nil || s.snapshotReader == nil || s.wallet == nil || s.watermarkWallet == nil || s.repo == nil {
		return nil, balancePreauthorizationUnavailable(errors.New("balance preauthorization dependency is unavailable"))
	}
	ctx = nonNilContext(ctx)

	estimate, err := s.estimateHold(ctx, request)
	if err != nil {
		return nil, balancePreauthorizationUnavailable(err)
	}
	holdAmount := estimate.HoldAmount
	outputWindow := estimate.OutputWindow
	requestID := strings.TrimSpace(request.RequestID)
	fingerprint := strings.TrimSpace(request.AuthorizationFingerprint)
	record, err := s.repo.PrepareBalancePreauthorization(ctx, &BalancePreauthorizationCommand{
		RequestID:                requestID,
		APIKeyID:                 request.APIKeyID,
		UserID:                   request.UserID,
		AuthorizationFingerprint: fingerprint,
		HoldAmount:               holdAmount,
		ExpiresAt:                request.ExpiresAt,
	})
	if err != nil {
		return nil, balancePreauthorizationUnavailable(err)
	}
	if record == nil || record.RequestID != requestID || record.APIKeyID != request.APIKeyID ||
		record.UserID != request.UserID || record.HoldAmount != holdAmount ||
		(record.Status != BalanceSettlementPrepared && record.Status != BalanceSettlementAuthorized) {
		return nil, balancePreauthorizationUnavailable(fmt.Errorf("unexpected balance preauthorization state: %v", balancePreauthorizationRecordStatus(record)))
	}

	attemptID := BalancePreauthorizationAttemptID(requestID, request.APIKeyID)
	snapshot, err := s.snapshotReader.LoadLiveBalanceInitializationSnapshot(ctx, request.UserID, requestID, request.APIKeyID)
	if err != nil {
		return nil, balancePreauthorizationUnavailable(fmt.Errorf("load live balance snapshot: %w", err))
	}
	// A cold wallet can be reconstructed only with no unsettled usage hold.
	// Existing wallets are never assigned the snapshot's absolute balance.
	authorized, err := s.watermarkWallet.AuthorizeLiveBalanceAtSnapshotWatermarkIfSafe(
		ctx, request.UserID, attemptID, snapshot.Balance, snapshot.Watermark,
		record.HoldAmount, !snapshot.HasUnsettled,
	)
	if err != nil {
		s.compensateAuthorizationFailure(requestID, request.APIKeyID, request.UserID)
		return nil, balancePreauthorizationUnavailable(fmt.Errorf("authorize live balance: %w", err))
	}
	if authorized.Outcome == LiveBalanceOutcomeInsufficient {
		s.compensateAuthorizationFailure(requestID, request.APIKeyID, request.UserID)
		return nil, ErrBalanceWithholdingFailed
	}
	if !liveBalanceAuthorizationSucceeded(authorized, record.HoldAmount) {
		s.compensateAuthorizationFailure(requestID, request.APIKeyID, request.UserID)
		return nil, balancePreauthorizationUnavailable(fmt.Errorf(
			"authorize live balance returned outcome=%d state=%d",
			authorized.Outcome,
			authorized.State,
		))
	}
	if err := s.repo.MarkBalancePreauthorizationAuthorized(ctx, requestID, request.APIKeyID); err != nil {
		s.compensateAuthorizationFailure(requestID, request.APIKeyID, request.UserID)
		return nil, balancePreauthorizationUnavailable(err)
	}

	core := &balancePreauthorizationGuardCore{
		service:       s,
		requestID:     requestID,
		apiKeyID:      request.APIKeyID,
		userID:        request.UserID,
		attemptID:     attemptID,
		holdAmount:    record.HoldAmount,
		outputWindow:  outputWindow,
		ownerToken:    1,
		terminalState: balancePreauthorizationGuardActive,
	}
	// Streaming top-up tracker: only for token-metered requests with a positive
	// output window and non-free output. NewBillingOutputHoldTracker returns nil
	// otherwise, so the hot path stays a no-op for per-request and free traffic.
	core.outputHoldTracker = NewBillingOutputHoldTracker(
		outputWindow,
		outputWindow,
		record.HoldAmount,
		estimate.OutputUnitPrice,
		1,
	)
	return &BalancePreauthorizationGuard{core: core, ownerToken: 1}, nil
}

// Resume reconstructs a guard for an exact active authorization or an identical
// finalization retry. Settled and refunded rows are explicit outcomes so an
// async response can distinguish a completed charge from an unsafe conflict.
func (s *BalancePreauthorizationService) Resume(ctx context.Context, request BalancePreauthorizationResumeRequest) (*BalancePreauthorizationGuard, error) {
	if s == nil || s.wallet == nil || s.repo == nil {
		return nil, balancePreauthorizationUnavailable(errors.New("balance preauthorization resume dependency is unavailable"))
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.AuthorizationFingerprint = strings.TrimSpace(request.AuthorizationFingerprint)
	if request.RequestID == "" || request.AuthorizationFingerprint == "" || request.APIKeyID <= 0 || request.UserID <= 0 || invalidNonnegativeMoney(request.HoldAmount) {
		return nil, balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
	}
	request.HoldAmount = QuantizeUsageBillingAmount(request.HoldAmount)
	ctx = nonNilContext(ctx)
	record, err := s.repo.PrepareBalancePreauthorization(ctx, &BalancePreauthorizationCommand{
		RequestID: request.RequestID, APIKeyID: request.APIKeyID, UserID: request.UserID,
		AuthorizationFingerprint: request.AuthorizationFingerprint, HoldAmount: request.HoldAmount,
	})
	if err != nil || record == nil || record.RequestID != request.RequestID || record.APIKeyID != request.APIKeyID ||
		record.UserID != request.UserID || record.AuthorizationFingerprint != request.AuthorizationFingerprint ||
		QuantizeUsageBillingAmount(record.HoldAmount) != request.HoldAmount {
		if err == nil {
			err = ErrUsageBillingRequestConflict
		}
		return nil, balancePreauthorizationUnavailable(fmt.Errorf("resume balance preauthorization: %w", err))
	}
	if record.Status == BalanceSettlementRefunded {
		return nil, ErrBalancePreauthorizationAlreadyRefunded
	}
	if record.Status == BalanceSettlementPending || record.Status == BalanceSettlementApplied || record.Status == BalanceSettlementTerminal {
		return nil, ErrBalancePreauthorizationAlreadyFinalized
	}
	resumedHoldAmount := request.HoldAmount
	switch record.Status {
	case BalanceSettlementAuthorized:
		if !record.ExpiresAt.After(time.Now()) {
			return nil, balancePreauthorizationUnavailable(fmt.Errorf("resume balance preauthorization: %w", ErrUsageBillingRequestConflict))
		}
	case BalanceSettlementFinalizationPending:
		if invalidNonnegativeMoney(record.Amount) {
			return nil, balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
		}
		// The Redis marker may already be finalized at this durable amount. Give
		// the retry that exact effective hold so applyUsageBilling does not try to
		// top it up before replaying the idempotent finalization transition.
		resumedHoldAmount = QuantizeUsageBillingAmount(record.Amount)
	default:
		return nil, balancePreauthorizationUnavailable(fmt.Errorf("resume balance preauthorization: %w", ErrUsageBillingRequestConflict))
	}
	return &BalancePreauthorizationGuard{core: &balancePreauthorizationGuardCore{
		service: s, requestID: request.RequestID, apiKeyID: request.APIKeyID, userID: request.UserID,
		attemptID: BalancePreauthorizationAttemptID(request.RequestID, request.APIKeyID), holdAmount: resumedHoldAmount,
		ownerToken: 1, terminalState: balancePreauthorizationGuardActive,
	}, ownerToken: 1}, nil
}

// balancePreauthorizationEstimate is the frozen pricing result of a request.
// OutputUnitPrice is the effective per-output-token price (after all pricing
// policy), used to plan streaming top-ups; it is zero for per-request endpoints
// and for free output, in which case no streaming tracker is created.
type balancePreauthorizationEstimate struct {
	HoldAmount      float64
	OutputWindow    int
	OutputUnitPrice float64
}

// estimateHold dispatches to the pricing strategy named by the request. Both
// strategies resolve pricing before any billing-state mutation and fail closed
// (returning an error) rather than emitting a zero hold for an unknown model.
func (s *BalancePreauthorizationService) estimateHold(
	ctx context.Context,
	request BalancePreauthorizationRequest,
) (balancePreauthorizationEstimate, error) {
	if request.EstimateKind == PreauthorizationEstimateFixed {
		if invalidNonnegativeMoney(request.FixedAmount) {
			return balancePreauthorizationEstimate{}, ErrInvalidBillingPreauthorizationEstimate
		}
		return balancePreauthorizationEstimate{HoldAmount: quantizeBillingHoldUpFromFloat(request.FixedAmount)}, nil
	}
	base, err := s.resolvedPricingCostInput(ctx, request.CostInput)
	if err != nil {
		return balancePreauthorizationEstimate{}, err
	}
	request.CostInput = base
	if base.Resolved != nil {
		switch base.Resolved.Mode {
		case BillingModePerRequest, BillingModeImage, BillingModeVideo:
			return s.estimatePerRequestHold(ctx, request)
		default:
			return s.estimateTokenUpperBoundHold(ctx, request)
		}
	}
	if request.EstimateKind == PreauthorizationEstimatePerRequest {
		return s.estimatePerRequestHold(ctx, request)
	}
	return s.estimateTokenUpperBoundHold(ctx, request)
}

// estimateTokenUpperBoundHold prices chat/text traffic from the current
// request's local token estimate. The reserve uses the normal input price and
// an explicit output limit (or bounded window), then finalization reconciles it
// to provider-reported usage. No historical usage query belongs on this path.
func (s *BalancePreauthorizationService) estimateTokenUpperBoundHold(
	ctx context.Context,
	request BalancePreauthorizationRequest,
) (balancePreauthorizationEstimate, error) {
	outputWindow := request.InitialOutputWindowTokens
	if outputWindow == 0 && !request.DisableOutputReservation {
		outputWindow = DefaultBalancePreauthorizationOutputWindow
	}
	base, err := s.resolvedPricingCostInput(ctx, request.CostInput)
	if err != nil {
		return balancePreauthorizationEstimate{}, err
	}
	inputTokens := request.EstimatedInputTokens
	if inputTokens <= 0 {
		inputTokens = request.BillableInputBytes
	}
	scenarios := []UsageTokens{
		{InputTokens: inputTokens, OutputTokens: outputWindow},
		{CacheReadTokens: inputTokens, OutputTokens: outputWindow},
		{CacheCreationTokens: inputTokens, CacheCreation5mTokens: inputTokens, OutputTokens: outputWindow},
		{CacheCreationTokens: inputTokens, CacheCreation1hTokens: inputTokens, OutputTokens: outputWindow},
	}
	maxCost := -1.0
	maxTokens := scenarios[0]
	for _, tokens := range scenarios {
		input := base
		input.Tokens = tokens
		breakdown, priceErr := s.costCalculator.CalculateCostUnified(input)
		if priceErr != nil {
			return balancePreauthorizationEstimate{}, priceErr
		}
		if breakdown == nil || invalidNonnegativeMoney(breakdown.ActualCost) {
			return balancePreauthorizationEstimate{}, ErrInvalidBillingPreauthorizationEstimate
		}
		if breakdown.ActualCost > maxCost {
			maxCost = breakdown.ActualCost
			maxTokens = tokens
		}
	}

	outputUnitPrice, err := s.estimateOutputUnitPrice(base, maxTokens, outputWindow, maxCost)
	if err != nil {
		return balancePreauthorizationEstimate{}, err
	}
	return balancePreauthorizationEstimate{
		HoldAmount:      quantizeBillingHoldUpFromFloat(maxCost),
		OutputWindow:    outputWindow,
		OutputUnitPrice: outputUnitPrice,
	}, nil
}

// estimateOutputUnitPrice derives the effective per-output-token price from a
// windowed-minus-baseline cost difference. Both prices use the same input
// disposition selected for the maximum hold and differ only in output tokens,
// so cache pricing cannot leak into streaming top-up targets.
// Returns zero (top-ups disabled) for non-positive results.
func (s *BalancePreauthorizationService) estimateOutputUnitPrice(
	base CostInput,
	windowedTokens UsageTokens,
	outputWindow int,
	windowedCost float64,
) (float64, error) {
	if outputWindow <= 0 {
		return 0, nil
	}
	baseline := base
	windowedTokens.OutputTokens = 0
	baseline.Tokens = windowedTokens
	baselineCost, err := s.costCalculator.CalculateCostUnified(baseline)
	if err != nil {
		return 0, err
	}
	if invalidNonnegativeMoney(windowedCost) || baselineCost == nil || invalidNonnegativeMoney(baselineCost.ActualCost) {
		return 0, ErrInvalidBillingPreauthorizationEstimate
	}
	delta := windowedCost - baselineCost.ActualCost
	// delta<=0 表示在当前定价下，输出窗口未产生额外边际成本（免费输出，或已并入
	// 打包/按次价，由 maxCost 场景 hold 覆盖）。此时返回 0 会使 OutputUnitPrice=0，
	// NewBillingOutputHoldTracker 返回 nil（见 billing_output_hold_tracker.go），
	// 从而禁用流式补扣——这是安全的：既然输出无边际计价，长流不会随长度增加欠扣，
	// 结算时仍按实际用量退差。
	// 前提1（守恒关键）：差分必须用与 baseline 同一输入处置（当前为 maxTokens）的
	// windowed 成本，否则会把输入/缓存侧价差混入输出单价，
	// 污染补扣目标额。切勿对真实计费的输出误禁补扣。
	// 前提2：差分仅在 outputWindow 处单点采样边际价，故"长流不欠扣"依赖输出边际价在
	// 整段流长上均匀；若将来出现阶梯/阈值型输出定价（窗口内免费、越过阈值才计费），
	// 该分支需改为不在此禁用补扣，否则长流会欠扣。
	if delta <= 0 {
		return 0, nil
	}
	return delta / float64(outputWindow), nil
}

// estimatePerRequestHold prices count/size/duration-metered endpoints once
// through the unified cost engine's per-request path. The reserved hold is the
// exact request price; settlement later refunds any positive difference. No
// output window applies, so the reserved-token field is reported as zero.
func (s *BalancePreauthorizationService) estimatePerRequestHold(
	ctx context.Context,
	request BalancePreauthorizationRequest,
) (balancePreauthorizationEstimate, error) {
	base, err := s.resolvedPricingCostInput(ctx, request.CostInput)
	if err != nil {
		return balancePreauthorizationEstimate{}, err
	}
	base.RequestCount = request.PerRequestEstimate.RequestCount
	base.UsageUnits = request.PerRequestEstimate.UsageUnits
	base.SizeTier = request.PerRequestEstimate.SizeTier
	if base.RequestCount <= 0 && base.UsageUnits <= 0 {
		base.RequestCount = 1
	}

	breakdown, err := s.costCalculator.CalculateCostUnified(base)
	if err != nil {
		return balancePreauthorizationEstimate{}, err
	}
	if breakdown == nil || invalidNonnegativeMoney(breakdown.ActualCost) {
		return balancePreauthorizationEstimate{}, ErrInvalidBillingPreauthorizationEstimate
	}
	return balancePreauthorizationEstimate{
		HoldAmount:   quantizeBillingHoldUpFromFloat(breakdown.ActualCost),
		OutputWindow: 0,
	}, nil
}

// resolvedPricingCostInput freezes the pricing resolution once so an unknown
// paid model fails closed before any wallet mutation. A missing resolver is
// left untouched: CalculateCostUnified falls back to its legacy pricing path.
// resolvedPricingCostInput 冻结一次 Resolver.Resolve 调用，确保后续 4 个 scenario +
// output baseline 共 5 次定价对齐到同一定价快照，避免请求内定价缓存刷新导致跨
// scenario 取 max 不一致。明确禁止未来 optimizer 按 scenario 重新 Resolve，以防
// 重新引入每 scenario 的 I/O 往返与快照漂移。
func (s *BalancePreauthorizationService) resolvedPricingCostInput(
	ctx context.Context,
	base CostInput,
) (CostInput, error) {
	base.Ctx = ctx
	if base.Resolved == nil && base.Resolver != nil {
		base.Resolved = base.Resolver.Resolve(ctx, PricingInput{
			Model:   base.Model,
			GroupID: base.GroupID,
			Group:   base.Group,
		})
		if base.Resolved == nil {
			return CostInput{}, ErrModelPricingUnavailable
		}
	}
	return base, nil
}

// compensateAuthorizationFailure 在授权阶段任何不确定结果（授权报错/余额不足/结果校验失败/
// MarkAuthorized 失败）时回滚。此路径必发生在上游请求之前且记录仍处 Prepared 态，故"全额退回
// 钱包 hold + 将 PG 预扣转入退款态"是守恒安全的：上游未产生费用则全额退款，用户不会被扣款却无对应请求。
// 该补偿是尽力而为：用独立后台 ctx（不随请求 ctx 取消，保证客户端断开仍退款）+ 3s 有界超时，
// 且忽略 begin/refund/complete 的错误——因为 PG 记录持久，失败时记录仍停留在可恢复的非终态，
// balance preauthorization 恢复 worker 会将其重新全额退款收敛（仅 Prepared 态才允许放弃授权退款；
// 已 Authorized 的记录由恢复路径改为全额结算，避免把崩溃窗口变成免费调用），因此这里既不阻塞也不永久卡死请求路径。
func (s *BalancePreauthorizationService) compensateAuthorizationFailure(requestID string, apiKeyID, userID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), balancePreauthorizationCompensationTimeout)
	defer cancel()
	beginErr := s.repo.BeginBalancePreauthorizationRefund(ctx, requestID, apiKeyID)
	result, refundErr := s.wallet.RefundLiveBalance(ctx, userID, BalancePreauthorizationAttemptID(requestID, apiKeyID))
	if beginErr != nil || refundErr != nil {
		return
	}
	if result.Outcome == LiveBalanceOutcomeNotFound || liveBalanceRefundSucceeded(result) {
		if s.repo.CompleteBalancePreauthorizationRefund(ctx, requestID, apiKeyID) == nil {
			s.cleanupLiveBalanceAttempt(ctx, userID, BalancePreauthorizationAttemptID(requestID, apiKeyID))
		}
	}
}

func validateBalancePreauthorizationRequest(request *BalancePreauthorizationRequest) error {
	if request == nil || strings.TrimSpace(request.RequestID) == "" ||
		strings.TrimSpace(request.AuthorizationFingerprint) == "" ||
		request.APIKeyID <= 0 || request.UserID <= 0 ||
		request.BillableInputBytes < 0 || request.EstimatedInputTokens < 0 || request.InitialOutputWindowTokens < 0 ||
		invalidNonnegativeMoney(request.FixedAmount) || (!request.ExpiresAt.IsZero() && !request.ExpiresAt.After(time.Now())) {
		return ErrInvalidBillingPreauthorizationEstimate
	}
	if request.BillingType != BillingTypeBalance {
		return fmt.Errorf("unsupported billing type %d", request.BillingType)
	}
	return nil
}

func liveBalanceOperationSucceeded(result LiveBalanceResult, expected LiveBalanceAttemptState) bool {
	return (result.Outcome == LiveBalanceOutcomeApplied || result.Outcome == LiveBalanceOutcomeIdempotent) &&
		result.State == expected
}

func liveBalanceAuthorizationSucceeded(result LiveBalanceResult, holdAmount float64) bool {
	return liveBalanceOperationSucceeded(result, LiveBalanceAttemptAuthorized) &&
		!invalidNonnegativeMoney(result.ReservedAmount) &&
		QuantizeUsageBillingAmount(result.ReservedAmount) >= QuantizeUsageBillingAmount(holdAmount)
}

func liveBalanceFinalizationSucceeded(result LiveBalanceResult, actualAmount float64) bool {
	return liveBalanceOperationSucceeded(result, LiveBalanceAttemptFinalized) &&
		!invalidNonnegativeMoney(result.ActualAmount) &&
		QuantizeUsageBillingAmount(result.ActualAmount) == QuantizeUsageBillingAmount(actualAmount)
}

func liveBalanceRefundSucceeded(result LiveBalanceResult) bool {
	return liveBalanceOperationSucceeded(result, LiveBalanceAttemptRefunded) &&
		!invalidNonnegativeMoney(result.ActualAmount) &&
		QuantizeUsageBillingAmount(result.ActualAmount) == 0
}

func balancePreauthorizationRecordStatus(record *BalancePreauthorizationRecord) any {
	if record == nil {
		return nil
	}
	return record.Status
}

func balancePreauthorizationUnavailable(cause error) error {
	if cause == nil {
		cause = errors.New("unknown balance preauthorization failure")
	}
	return ErrBillingServiceUnavailable.WithCause(cause)
}

func quantizeBillingHoldUpFromFloat(value float64) float64 {
	return quantizeBillingHoldUp(decimal.NewFromFloat(value))
}

func BalancePreauthorizationAttemptID(requestID string, apiKeyID int64) string {
	return strings.TrimSpace(requestID) + ":" + strconv.FormatInt(apiKeyID, 10)
}
