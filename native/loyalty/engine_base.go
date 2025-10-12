package loyalty

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"nhbchain/core/types"
)

const (
	eventBaseAccrued = "loyalty.base.accrued"
	eventBaseSkipped = "loyalty.base.skipped"
)

// BaseRewardState describes the minimal functionality the loyalty engine needs
// from the surrounding state implementation.
type BaseRewardState interface {
	LoyaltyGlobalConfig() (*GlobalConfig, error)
	GetAccount(addr []byte) (*types.Account, error)
	PutAccount(addr []byte, account *types.Account) error
	LoyaltyBaseDailyAccrued(addr []byte, day string) (*big.Int, error)
	SetLoyaltyBaseDailyAccrued(addr []byte, day string, amount *big.Int) error
	LoyaltyBaseTotalAccrued(addr []byte) (*big.Int, error)
	SetLoyaltyBaseTotalAccrued(addr []byte, amount *big.Int) error
	AppendEvent(evt *types.Event)
	QueuePendingBaseReward(ctx *BaseRewardContext, reward *big.Int)
}

// BaseRewardContext captures the transaction metadata needed to evaluate the
// base spend reward.
type BaseRewardContext struct {
	TxHash      [32]byte
	From        []byte
	To          []byte
	Token       string
	Amount      *big.Int
	Timestamp   time.Time
	FromAccount *types.Account
	ToAccount   *types.Account
}

func (ctx *BaseRewardContext) amountValue() *big.Int {
	if ctx == nil || ctx.Amount == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(ctx.Amount)
}

func (ctx *BaseRewardContext) dayKey() string {
	if ctx == nil {
		return ""
	}
	return ctx.Timestamp.UTC().Format("2006-01-02")
}

func (ctx *BaseRewardContext) baseEventAttributes() map[string]string {
	attrs := map[string]string{
		"day":   ctx.dayKey(),
		"token": strings.ToUpper(ctx.Token),
		"amount": func() string {
			if ctx.Amount == nil {
				return "0"
			}
			return ctx.Amount.String()
		}(),
	}
	if len(ctx.From) > 0 {
		attrs["from"] = hex.EncodeToString(ctx.From)
	}
	if len(ctx.To) > 0 {
		attrs["to"] = hex.EncodeToString(ctx.To)
	}
	return attrs
}

func emitSkip(st BaseRewardState, ctx *BaseRewardContext, reason string, extra map[string]string) {
	if st == nil || ctx == nil {
		return
	}
	attrs := ctx.baseEventAttributes()
	attrs["reason"] = reason
	for k, v := range extra {
		attrs[k] = v
	}
	st.AppendEvent(&types.Event{Type: eventBaseSkipped, Attributes: attrs})
}

func emitAccrued(st BaseRewardState, ctx *BaseRewardContext, basisPoints uint32, reward *big.Int) {
	if st == nil || ctx == nil {
		return
	}
	attrs := ctx.baseEventAttributes()
	attrs["reward"] = reward.String()
	attrs["baseBps"] = strconv.FormatUint(uint64(basisPoints), 10)
	st.AppendEvent(&types.Event{Type: eventBaseAccrued, Attributes: attrs})
}

// ApplyBaseReward evaluates and, if applicable, applies the base spend reward.
func (e *Engine) ApplyBaseReward(st BaseRewardState, ctx *BaseRewardContext) {
	if st == nil || ctx == nil {
		return
	}
	cfg, err := st.LoyaltyGlobalConfig()
	if err != nil {
		emitSkip(st, ctx, "config_error", map[string]string{"error": err.Error()})
		return
	}
	if cfg == nil || !cfg.Active {
		emitSkip(st, ctx, "inactive", nil)
		return
	}
	cfg = cfg.Clone().Normalize()
	if ctx.FromAccount == nil {
		emitSkip(st, ctx, "missing_from_account", nil)
		return
	}
	if strings.ToUpper(strings.TrimSpace(ctx.Token)) != "NHB" {
		emitSkip(st, ctx, "token_not_supported", map[string]string{"token": ctx.Token})
		return
	}
	if len(ctx.From) == 0 || len(cfg.Treasury) == 0 {
		emitSkip(st, ctx, "invalid_addresses", nil)
		return
	}
	if len(ctx.From) == len(ctx.To) && len(ctx.From) > 0 && len(ctx.To) > 0 &&
		string(ctx.From) == string(ctx.To) {
		emitSkip(st, ctx, "self_transfer", nil)
		return
	}
	amount := ctx.amountValue()
	if amount.Sign() <= 0 {
		emitSkip(st, ctx, "amount_not_positive", nil)
		return
	}
	if amount.Cmp(cfg.MinSpend) < 0 {
		emitSkip(st, ctx, "below_min_spend", map[string]string{"minSpend": cfg.MinSpend.String()})
		return
	}
	if cfg.BaseBps == 0 {
		emitSkip(st, ctx, "no_reward_rate", nil)
		return
	}

	reward := new(big.Int).Mul(amount, new(big.Int).SetUint64(uint64(cfg.BaseBps)))
	reward = reward.Quo(reward, big.NewInt(int64(BaseRewardBpsDenominator)))
	if reward.Sign() <= 0 {
		emitSkip(st, ctx, "reward_zero", nil)
		return
	}
	if cfg.CapPerTx.Sign() > 0 && reward.Cmp(cfg.CapPerTx) > 0 {
		reward = new(big.Int).Set(cfg.CapPerTx)
	}

	dayKey := ctx.dayKey()
	if cfg.DailyCapUser.Sign() > 0 && dayKey != "" {
		accruedToday, err := st.LoyaltyBaseDailyAccrued(ctx.From, dayKey)
		if err != nil {
			emitSkip(st, ctx, "meter_error", map[string]string{"error": err.Error()})
			return
		}
		remaining := new(big.Int).Sub(cfg.DailyCapUser, accruedToday)
		if remaining.Sign() <= 0 {
			emitSkip(st, ctx, "daily_cap_reached", map[string]string{"dailyCap": cfg.DailyCapUser.String()})
			return
		}
		if reward.Cmp(remaining) > 0 {
			reward = remaining
		}
	}
	if reward.Sign() <= 0 {
		emitSkip(st, ctx, "reward_zero", nil)
		return
	}

	treasuryAcc, err := st.GetAccount(cfg.Treasury)
	if err != nil {
		emitSkip(st, ctx, "treasury_error", map[string]string{"error": err.Error()})
		return
	}
	if treasuryAcc.BalanceZNHB == nil {
		treasuryAcc.BalanceZNHB = big.NewInt(0)
	}
	if treasuryAcc.BalanceZNHB.Cmp(reward) < 0 {
		emitSkip(st, ctx, "treasury_insufficient", map[string]string{"available": treasuryAcc.BalanceZNHB.String()})
		return
	}

	st.QueuePendingBaseReward(ctx, reward)

	if dayKey != "" {
		accruedToday, err := st.LoyaltyBaseDailyAccrued(ctx.From, dayKey)
		if err != nil {
			emitSkip(st, ctx, "meter_error", map[string]string{"error": err.Error()})
			return
		}
		newDaily := new(big.Int).Add(accruedToday, reward)
		if err := st.SetLoyaltyBaseDailyAccrued(ctx.From, dayKey, newDaily); err != nil {
			emitSkip(st, ctx, "meter_error", map[string]string{"error": err.Error()})
			return
		}
	}
	totalAccrued, err := st.LoyaltyBaseTotalAccrued(ctx.From)
	if err != nil {
		emitSkip(st, ctx, "meter_error", map[string]string{"error": err.Error()})
		return
	}
	newTotal := new(big.Int).Add(totalAccrued, reward)
	if err := st.SetLoyaltyBaseTotalAccrued(ctx.From, newTotal); err != nil {
		emitSkip(st, ctx, "meter_error", map[string]string{"error": err.Error()})
		return
	}

	emitAccrued(st, ctx, cfg.BaseBps, reward)
}

// MustBaseReward returns the reward amount that would be paid out for the given
// amount without considering treasury availability or caps. It is intended for
// testing and diagnostics.
func (c *GlobalConfig) MustBaseReward(amount *big.Int) *big.Int {
	if c == nil {
		return big.NewInt(0)
	}
	normalized := c.Clone().Normalize()
	if normalized.BaseBps == 0 || amount == nil {
		return big.NewInt(0)
	}
	reward := new(big.Int).Mul(amount, new(big.Int).SetUint64(uint64(normalized.BaseBps)))
	return reward.Quo(reward, big.NewInt(int64(BaseRewardBpsDenominator)))
}

// ValidateBaseRewardContext performs sanity checks that are useful for testing.
func ValidateBaseRewardContext(ctx *BaseRewardContext) error {
	if ctx == nil {
		return fmt.Errorf("nil context")
	}
	if ctx.FromAccount == nil {
		return fmt.Errorf("context missing from account")
	}
	if ctx.Amount == nil {
		return fmt.Errorf("context amount must be set")
	}
	return nil
}
