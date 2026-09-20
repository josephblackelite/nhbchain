package modules

// The replay as it was at release/hardening-r1: a walk over every height from 1
// to the tip. The tests compare the current replay, which visits only the blocks
// that hold a lending transaction, against it. Kept verbatim (comments aside).

import (
	"fmt"
	"math/big"
	"strings"

	"nhbchain/core/types"
	"nhbchain/native/lending"
)

func (m *LendingModule) legacyReplayCommittedPoolState(poolID string) (*lending.Market, map[[20]byte]*lending.UserAccount, error) {
	if m == nil || m.node == nil {
		return nil, nil, fmt.Errorf("lending module unavailable")
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	market := m.defaultMarket(id)
	market.SupplyIndex = new(big.Int).Set(lendingRay)
	market.BorrowIndex = new(big.Int).Set(lendingRay)
	market.BorrowedThisBlock = big.NewInt(0)
	market.OracleMedianWei = big.NewInt(0)
	market.OraclePrevMedianWei = big.NewInt(0)
	users := make(map[[20]byte]*lending.UserAccount)
	var sawLendingTx bool
	height := m.node.GetHeight()
	for blockHeight := uint64(1); blockHeight <= height; blockHeight++ {
		block, err := m.node.GetBlockByHeight(blockHeight)
		if err != nil || block == nil {
			continue
		}
		for _, tx := range block.Transactions {
			if tx == nil || !isCommittedLendingTxType(tx.Type) {
				continue
			}
			txPoolID, err := lendingPoolIDFromTxData(tx.Data)
			if err != nil || txPoolID != id {
				continue
			}
			from, err := tx.From()
			if err != nil || len(from) == 0 {
				continue
			}
			var addr [20]byte
			copy(addr[:], from)
			user := users[addr]
			if user == nil {
				user = &lending.UserAccount{
					Address:        toCryptoAddress(addr),
					CollateralZNHB: big.NewInt(0),
					SupplyShares:   big.NewInt(0),
					DebtNHB:        big.NewInt(0),
					ScaledDebt:     big.NewInt(0),
				}
				users[addr] = user
			}
			amount := cloneBigInt(tx.Value)
			switch tx.Type {
			case types.TxTypeLendingSupplyNHB:
				user.SupplyShares = sumBigInt(user.SupplyShares, amount)
				market.TotalSupplyShares = sumBigInt(market.TotalSupplyShares, amount)
				market.TotalNHBSupplied = sumBigInt(market.TotalNHBSupplied, amount)
			case types.TxTypeLendingWithdrawNHB:
				if user.SupplyShares.Cmp(amount) < 0 {
					user.SupplyShares = big.NewInt(0)
				} else {
					user.SupplyShares = new(big.Int).Sub(user.SupplyShares, amount)
				}
				if market.TotalSupplyShares.Cmp(amount) < 0 {
					market.TotalSupplyShares = big.NewInt(0)
				} else {
					market.TotalSupplyShares = new(big.Int).Sub(market.TotalSupplyShares, amount)
				}
				if market.TotalNHBSupplied.Cmp(amount) < 0 {
					market.TotalNHBSupplied = big.NewInt(0)
				} else {
					market.TotalNHBSupplied = new(big.Int).Sub(market.TotalNHBSupplied, amount)
				}
			case types.TxTypeLendingDepositZNHB:
				user.CollateralZNHB = sumBigInt(user.CollateralZNHB, amount)
			case types.TxTypeLendingWithdrawZNHB:
				if user.CollateralZNHB.Cmp(amount) < 0 {
					user.CollateralZNHB = big.NewInt(0)
				} else {
					user.CollateralZNHB = new(big.Int).Sub(user.CollateralZNHB, amount)
				}
			case types.TxTypeLendingBorrowNHB:
				user.DebtNHB = sumBigInt(user.DebtNHB, amount)
				user.ScaledDebt = sumBigInt(user.ScaledDebt, amount)
				market.TotalNHBBorrowed = sumBigInt(market.TotalNHBBorrowed, amount)
			case types.TxTypeLendingRepayNHB:
				repayAmount := amount
				if user.DebtNHB.Cmp(repayAmount) < 0 {
					repayAmount = new(big.Int).Set(user.DebtNHB)
				}
				if repayAmount.Sign() > 0 {
					user.DebtNHB = new(big.Int).Sub(user.DebtNHB, repayAmount)
					if user.ScaledDebt.Cmp(repayAmount) < 0 {
						user.ScaledDebt = big.NewInt(0)
					} else {
						user.ScaledDebt = new(big.Int).Sub(user.ScaledDebt, repayAmount)
					}
					if market.TotalNHBBorrowed.Cmp(repayAmount) < 0 {
						market.TotalNHBBorrowed = big.NewInt(0)
					} else {
						market.TotalNHBBorrowed = new(big.Int).Sub(market.TotalNHBBorrowed, repayAmount)
					}
				}
			}
			market.LastUpdateBlock = blockHeight
			sawLendingTx = true
		}
	}
	if !sawLendingTx {
		return nil, nil, nil
	}
	return market, users, nil
}
