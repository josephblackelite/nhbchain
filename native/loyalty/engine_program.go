package loyalty

import (
	"encoding/hex"
	"math/big"
	"strconv"
	"strings"
	"time"

	"nhbchain/core/types"
)

const (
	eventProgramAccrued       = "loyalty.program.accrued"
	eventProgramSkipped       = "loyalty.program.skipped"
	eventProgramPaymasterWarn = "loyalty.program.paymaster_warning"

	resultAccrued             = "accrued"
	resultThrottledLowReserve = "throttled — low reserve"
)

// ProgramRewardState describes the additional state access required to process
// business-funded loyalty programs.
type ProgramRewardState interface {
	BaseRewardState
	LoyaltyProgramByID(id ProgramID) (*Program, bool, error)
	LoyaltyProgramsByOwner(owner [20]byte) ([]ProgramID, error)
	LoyaltyBusinessByMerchant(merchant [20]byte) (*Business, bool, error)
	LoyaltyProgramDailyAccrued(programID ProgramID, addr []byte, day string) (*big.Int, error)
	SetLoyaltyProgramDailyAccrued(programID ProgramID, addr []byte, day string, amount *big.Int) error
	LoyaltyProgramDailyTotalAccrued(programID ProgramID, day string) (*big.Int, error)
	SetLoyaltyProgramDailyTotalAccrued(programID ProgramID, day string, amount *big.Int) error
	LoyaltyProgramEpochAccrued(programID ProgramID, epoch uint64) (*big.Int, error)
	SetLoyaltyProgramEpochAccrued(programID ProgramID, epoch uint64, amount *big.Int) error
	LoyaltyProgramIssuanceAccrued(programID ProgramID, addr []byte) (*big.Int, error)
	SetLoyaltyProgramIssuanceAccrued(programID ProgramID, addr []byte, amount *big.Int) error
}

// ProgramRewardContext extends the base reward context with optional hints used
// when resolving programs.
type ProgramRewardContext struct {
	*BaseRewardContext
	ProgramHint *ProgramID
	Merchant    []byte
}

func (ctx *ProgramRewardContext) merchantBytes() []byte {
	if ctx == nil {
		return nil
	}
	if len(ctx.Merchant) > 0 {
		return ctx.Merchant
	}
	if ctx.BaseRewardContext != nil && len(ctx.BaseRewardContext.To) > 0 {
		return ctx.BaseRewardContext.To
	}
	return nil
}

func (ctx *ProgramRewardContext) dayKey() string {
	if ctx == nil || ctx.BaseRewardContext == nil {
		return ""
	}
	return ctx.BaseRewardContext.dayKey()
}

func (ctx *ProgramRewardContext) timestamp() time.Time {
	if ctx == nil || ctx.BaseRewardContext == nil {
		return time.Time{}
	}
	return ctx.BaseRewardContext.Timestamp
}

func (ctx *ProgramRewardContext) programEventAttributes(program *Program, business *Business) map[string]string {
	attrs := map[string]string{}
	if ctx != nil && ctx.BaseRewardContext != nil {
		baseAttrs := ctx.BaseRewardContext.baseEventAttributes()
		for k, v := range baseAttrs {
			attrs[k] = v
		}
	}
	if program != nil {
		attrs["programId"] = hex.EncodeToString(program.ID[:])
		attrs["accrualBps"] = strconv.FormatUint(uint64(program.AccrualBps), 10)
		token := strings.ToUpper(strings.TrimSpace(program.TokenSymbol))
		if token != "" {
			attrs["rewardToken"] = token
		}
		if !isZeroAddress(program.Owner) {
			attrs["programOwner"] = hex.EncodeToString(program.Owner[:])
		}
	}
	if business != nil && !isZeroAddress(business.Paymaster) {
		attrs["paymaster"] = hex.EncodeToString(business.Paymaster[:])
	}
	if merchant := ctx.merchantBytes(); len(merchant) == 20 {
		attrs["merchant"] = hex.EncodeToString(merchant)
	}
	return attrs
}

// ApplyProgramReward evaluates and applies a loyalty program reward when a
// matching program and funded paymaster are available.
func (e *Engine) ApplyProgramReward(st ProgramRewardState, ctx *ProgramRewardContext) string {
	if st == nil || ctx == nil || ctx.BaseRewardContext == nil {
		return ""
	}
	baseCtx := ctx.BaseRewardContext
	if baseCtx.FromAccount == nil {
		emitProgramSkip(st, ctx, nil, nil, "missing_from_account", nil)
		return "missing_from_account"
	}
	amount := baseCtx.amountValue()
	if amount.Sign() <= 0 {
		emitProgramSkip(st, ctx, nil, nil, "amount_not_positive", nil)
		return "amount_not_positive"
	}
	fromAddr := baseCtx.From
	if len(fromAddr) != 20 {
		emitProgramSkip(st, ctx, nil, nil, "invalid_from_address", map[string]string{"length": strconv.Itoa(len(fromAddr))})
		return "invalid_from_address"
	}
	timestamp := uint64(baseCtx.Timestamp.UTC().Unix())

	resolution, reason, extra := resolveProgram(st, ctx, timestamp)
	if reason != "" {
		emitProgramSkip(st, ctx, resolution.program, resolution.business, reason, extra)
		return reason
	}
	program := resolution.program
	business := resolution.business
	if program == nil || business == nil {
		emitProgramSkip(st, ctx, program, business, "program_not_found", nil)
		return "program_not_found"
	}
	if !programActiveForTimestamp(program, timestamp) {
		emitProgramSkip(st, ctx, program, business, "program_inactive", nil)
		return "program_inactive"
	}

	if token := strings.ToUpper(strings.TrimSpace(baseCtx.Token)); token != "NHB" {
		emitProgramSkip(st, ctx, program, business, "token_not_supported", map[string]string{"token": baseCtx.Token})
		return "token_not_supported"
	}
	if token := strings.ToUpper(strings.TrimSpace(program.TokenSymbol)); token != "ZNHB" {
		emitProgramSkip(st, ctx, program, business, "reward_token_not_supported", map[string]string{"token": program.TokenSymbol})
		return "reward_token_not_supported"
	}
	if program.MinSpendWei != nil && amount.Cmp(program.MinSpendWei) < 0 {
		emitProgramSkip(st, ctx, program, business, "below_min_spend", map[string]string{"minSpend": program.MinSpendWei.String()})
		return "below_min_spend"
	}
	if program.StartTime != 0 && timestamp < program.StartTime {
		emitProgramSkip(st, ctx, program, business, "not_started", map[string]string{"startTime": strconv.FormatUint(program.StartTime, 10)})
		return "not_started"
	}
	if program.EndTime != 0 && timestamp > program.EndTime {
		emitProgramSkip(st, ctx, program, business, "program_ended", map[string]string{"endTime": strconv.FormatUint(program.EndTime, 10)})
		return "program_ended"
	}
	if program.AccrualBps == 0 {
		emitProgramSkip(st, ctx, program, business, "no_reward_rate", nil)
		return "no_reward_rate"
	}

	reward := new(big.Int).Mul(amount, new(big.Int).SetUint64(uint64(program.AccrualBps)))
	reward = reward.Quo(reward, big.NewInt(10_000))
	if reward.Sign() <= 0 {
		emitProgramSkip(st, ctx, program, business, "reward_zero", nil)
		return "reward_zero"
	}
	if program.CapPerTx != nil && program.CapPerTx.Sign() > 0 && reward.Cmp(program.CapPerTx) > 0 {
		reward = new(big.Int).Set(program.CapPerTx)
	}

	dayKey := ctx.dayKey()
	var (
		accruedToday      *big.Int
		programDailyTotal *big.Int
		programEpochTotal *big.Int
		issuanceTotal     *big.Int
	)
	if program.DailyCapUser != nil && program.DailyCapUser.Sign() > 0 {
		if dayKey == "" {
			emitProgramSkip(st, ctx, program, business, "missing_day_key", nil)
			return "missing_day_key"
		}
		var err error
		accruedToday, err = st.LoyaltyProgramDailyAccrued(program.ID, fromAddr, dayKey)
		if err != nil {
			emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
			return "meter_error"
		}
		remaining := new(big.Int).Sub(program.DailyCapUser, accruedToday)
		if remaining.Sign() <= 0 {
			emitProgramSkip(st, ctx, program, business, "daily_cap_reached", map[string]string{"dailyCap": program.DailyCapUser.String()})
			return "daily_cap_reached"
		}
		if reward.Cmp(remaining) > 0 {
			reward = remaining
		}
	}
	if reward.Sign() <= 0 {
		emitProgramSkip(st, ctx, program, business, "reward_zero", nil)
		return "reward_zero"
	}

	if program.DailyCapProgram != nil && program.DailyCapProgram.Sign() > 0 {
		if dayKey == "" {
			emitProgramSkip(st, ctx, program, business, "missing_day_key", nil)
			return "missing_day_key"
		}
		var err error
		programDailyTotal, err = st.LoyaltyProgramDailyTotalAccrued(program.ID, dayKey)
		if err != nil {
			emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
			return "meter_error"
		}
		remaining := new(big.Int).Sub(program.DailyCapProgram, programDailyTotal)
		if remaining.Sign() <= 0 {
			emitProgramSkip(st, ctx, program, business, "daily_program_cap_reached", map[string]string{"dailyCap": program.DailyCapProgram.String()})
			return "daily_program_cap_reached"
		}
		if reward.Cmp(remaining) > 0 {
			reward = remaining
		}
	}

	var epochKey uint64
	if program.EpochCapProgram != nil && program.EpochCapProgram.Sign() > 0 {
		if program.EpochLengthSeconds == 0 {
			emitProgramSkip(st, ctx, program, business, "epoch_not_configured", nil)
			return "epoch_not_configured"
		}
		epochKey = timestamp / program.EpochLengthSeconds
		var err error
		programEpochTotal, err = st.LoyaltyProgramEpochAccrued(program.ID, epochKey)
		if err != nil {
			emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
			return "meter_error"
		}
		remaining := new(big.Int).Sub(program.EpochCapProgram, programEpochTotal)
		if remaining.Sign() <= 0 {
			emitProgramSkip(st, ctx, program, business, "epoch_cap_reached", map[string]string{"epochCap": program.EpochCapProgram.String(), "epoch": strconv.FormatUint(epochKey, 10)})
			return "epoch_cap_reached"
		}
		if reward.Cmp(remaining) > 0 {
			reward = remaining
		}
	}

	if reward.Sign() <= 0 {
		emitProgramSkip(st, ctx, program, business, "reward_zero", nil)
		return "reward_zero"
	}

	if program.IssuanceCapUser != nil && program.IssuanceCapUser.Sign() > 0 {
		var err error
		issuanceTotal, err = st.LoyaltyProgramIssuanceAccrued(program.ID, fromAddr)
		if err != nil {
			emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
			return "meter_error"
		}
		remaining := new(big.Int).Sub(program.IssuanceCapUser, issuanceTotal)
		if remaining.Sign() <= 0 {
			emitProgramSkip(st, ctx, program, business, "issuance_cap_reached", map[string]string{"issuanceCap": program.IssuanceCapUser.String()})
			return "issuance_cap_reached"
		}
		if reward.Cmp(remaining) > 0 {
			reward = remaining
		}
	}

	if isZeroAddress(business.Paymaster) {
		emitProgramSkip(st, ctx, program, business, "paymaster_missing", nil)
		return "paymaster_missing"
	}
	paymasterAcc, err := st.GetAccount(business.Paymaster[:])
	if err != nil {
		emitProgramSkip(st, ctx, program, business, "paymaster_error", map[string]string{"error": err.Error()})
		return "paymaster_error"
	}
	if paymasterAcc.BalanceZNHB == nil {
		paymasterAcc.BalanceZNHB = big.NewInt(0)
	}

	if business.PaymasterReserveMin != nil && business.PaymasterReserveMin.Sign() > 0 {
		projected := new(big.Int).Sub(paymasterAcc.BalanceZNHB, reward)
		warnThreshold := new(big.Int).Mul(business.PaymasterReserveMin, big.NewInt(120))
		warnThreshold = warnThreshold.Quo(warnThreshold, big.NewInt(100))
		if projected.Cmp(warnThreshold) <= 0 {
			emitPaymasterWarning(st, ctx, program, business, projected, business.PaymasterReserveMin)
		}
		if projected.Cmp(business.PaymasterReserveMin) < 0 {
			emitProgramSkip(st, ctx, program, business, resultThrottledLowReserve, map[string]string{
				"available":  paymasterAcc.BalanceZNHB.String(),
				"reserveMin": business.PaymasterReserveMin.String(),
			})
			return resultThrottledLowReserve
		}
	}
	if paymasterAcc.BalanceZNHB.Cmp(reward) < 0 {
		emitProgramSkip(st, ctx, program, business, "paymaster_insufficient", map[string]string{"available": paymasterAcc.BalanceZNHB.String()})
		return "paymaster_insufficient"
	}

	paymasterAcc.BalanceZNHB = new(big.Int).Sub(paymasterAcc.BalanceZNHB, reward)
	if err := st.PutAccount(business.Paymaster[:], paymasterAcc); err != nil {
		emitProgramSkip(st, ctx, program, business, "paymaster_persist_error", map[string]string{"error": err.Error()})
		return "paymaster_persist_error"
	}

	if baseCtx.FromAccount.BalanceZNHB == nil {
		baseCtx.FromAccount.BalanceZNHB = big.NewInt(0)
	}
	baseCtx.FromAccount.BalanceZNHB = new(big.Int).Add(baseCtx.FromAccount.BalanceZNHB, reward)

	if dayKey != "" {
		if accruedToday == nil {
			var err error
			accruedToday, err = st.LoyaltyProgramDailyAccrued(program.ID, fromAddr, dayKey)
			if err != nil {
				emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
				return "meter_error"
			}
		}
		newDaily := new(big.Int).Add(accruedToday, reward)
		if err := st.SetLoyaltyProgramDailyAccrued(program.ID, fromAddr, dayKey, newDaily); err != nil {
			emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
			return "meter_error"
		}
		if program.DailyCapProgram != nil && program.DailyCapProgram.Sign() > 0 {
			if programDailyTotal == nil {
				var err error
				programDailyTotal, err = st.LoyaltyProgramDailyTotalAccrued(program.ID, dayKey)
				if err != nil {
					emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
					return "meter_error"
				}
			}
			if err := st.SetLoyaltyProgramDailyTotalAccrued(program.ID, dayKey, new(big.Int).Add(programDailyTotal, reward)); err != nil {
				emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
				return "meter_error"
			}
		}
	}
	if program.EpochCapProgram != nil && program.EpochCapProgram.Sign() > 0 {
		if programEpochTotal == nil {
			var err error
			programEpochTotal, err = st.LoyaltyProgramEpochAccrued(program.ID, epochKey)
			if err != nil {
				emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
				return "meter_error"
			}
		}
		if err := st.SetLoyaltyProgramEpochAccrued(program.ID, epochKey, new(big.Int).Add(programEpochTotal, reward)); err != nil {
			emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
			return "meter_error"
		}
	}
	if program.IssuanceCapUser != nil && program.IssuanceCapUser.Sign() > 0 {
		if issuanceTotal == nil {
			var err error
			issuanceTotal, err = st.LoyaltyProgramIssuanceAccrued(program.ID, fromAddr)
			if err != nil {
				emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
				return "meter_error"
			}
		}
		if err := st.SetLoyaltyProgramIssuanceAccrued(program.ID, fromAddr, new(big.Int).Add(issuanceTotal, reward)); err != nil {
			emitProgramSkip(st, ctx, program, business, "meter_error", map[string]string{"error": err.Error()})
			return "meter_error"
		}
	}

	emitProgramAccrued(st, ctx, program, business, reward)
	return resultAccrued
}

type programResolution struct {
	program  *Program
	business *Business
}

func resolveProgram(st ProgramRewardState, ctx *ProgramRewardContext, timestamp uint64) (programResolution, string, map[string]string) {
	if st == nil || ctx == nil {
		return programResolution{}, "program_not_found", nil
	}
	merchantBytes := ctx.merchantBytes()
	var merchantAddr [20]byte
	if len(merchantBytes) == 20 {
		copy(merchantAddr[:], merchantBytes)
	}

	var (
		program  *Program
		business *Business
		err      error
	)

	if ctx.ProgramHint != nil {
		program, err = loadProgramByID(st, *ctx.ProgramHint)
		if err != nil {
			return programResolution{}, "program_lookup_error", map[string]string{"error": err.Error()}
		}
		if program == nil {
			return programResolution{}, "program_not_found", nil
		}
		if merchantBytes == nil || len(merchantBytes) != 20 {
			merchantAddr = program.Owner
		}
	} else {
		if len(merchantBytes) != 20 {
			return programResolution{}, "merchant_missing", nil
		}
		ids, err := st.LoyaltyProgramsByOwner(merchantAddr)
		if err != nil {
			return programResolution{}, "program_list_error", map[string]string{"error": err.Error()}
		}
		for _, id := range ids {
			candidate, err := loadProgramByID(st, id)
			if err != nil {
				return programResolution{}, "program_lookup_error", map[string]string{"error": err.Error()}
			}
			if candidate == nil || !candidate.Active {
				continue
			}
			if !programActiveForTimestamp(candidate, timestamp) {
				continue
			}
			program = candidate
			break
		}
		if program == nil {
			return programResolution{}, "program_not_found", nil
		}
	}

	lookupMerchant := merchantAddr
	if isZeroAddress(lookupMerchant) {
		lookupMerchant = program.Owner
	}
	if !isZeroAddress(lookupMerchant) {
		business, err = loadBusinessForMerchant(st, lookupMerchant)
		if err != nil {
			return programResolution{program: program}, "business_lookup_error", map[string]string{"error": err.Error()}
		}
	}
	if business == nil {
		return programResolution{program: program}, "business_not_found", nil
	}
	return programResolution{program: program, business: business}, "", nil
}

func loadProgramByID(st ProgramRewardState, id ProgramID) (*Program, error) {
	program, ok, err := st.LoyaltyProgramByID(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return program, nil
}

func loadBusinessForMerchant(st ProgramRewardState, merchant [20]byte) (*Business, error) {
	business, ok, err := st.LoyaltyBusinessByMerchant(merchant)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return business, nil
}

func programActiveForTimestamp(program *Program, timestamp uint64) bool {
	if program == nil || !program.Active {
		return false
	}
	if program.StartTime != 0 && timestamp < program.StartTime {
		return false
	}
	if program.EndTime != 0 && timestamp > program.EndTime {
		return false
	}
	return true
}

func emitProgramSkip(st ProgramRewardState, ctx *ProgramRewardContext, program *Program, business *Business, reason string, extra map[string]string) {
	if st == nil || ctx == nil {
		return
	}
	attrs := ctx.programEventAttributes(program, business)
	attrs["reason"] = reason
	for k, v := range extra {
		attrs[k] = v
	}
	st.AppendEvent(&types.Event{Type: eventProgramSkipped, Attributes: attrs})
}

func emitProgramAccrued(st ProgramRewardState, ctx *ProgramRewardContext, program *Program, business *Business, reward *big.Int) {
	if st == nil || ctx == nil || reward == nil {
		return
	}
	attrs := ctx.programEventAttributes(program, business)
	attrs["reward"] = reward.String()
	st.AppendEvent(&types.Event{Type: eventProgramAccrued, Attributes: attrs})
}

func emitPaymasterWarning(st ProgramRewardState, ctx *ProgramRewardContext, program *Program, business *Business, balance, reserve *big.Int) {
	if st == nil || ctx == nil {
		return
	}
	attrs := ctx.programEventAttributes(program, business)
	if balance != nil {
		attrs["balance"] = balance.String()
	}
	if reserve != nil {
		attrs["reserveMin"] = reserve.String()
	}
	st.AppendEvent(&types.Event{Type: eventProgramPaymasterWarn, Attributes: attrs})
}
