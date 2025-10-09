package system

import (
	"math/big"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/lending"
	"nhbchain/rpc/modules"
	"nhbchain/storage"
)

func TestLendingSupplyFailsWhenPaused(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	node.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 7500, LiquidationThreshold: 8000, LiquidationBonus: 500, DeveloperFeeCapBps: 100})
	node.SetLendingDeveloperFee(0, crypto.Address{})

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address()

	if err := seedPausedLendingState(node, userAddr); err != nil {
		t.Fatalf("seed lending state: %v", err)
	}

	before := snapshotLendingState(t, node, userAddr)

	node.SetModulePaused("lending", true)

	module := modules.NewLendingModule(node)
	var user [20]byte
	copy(user[:], userAddr.Bytes())
	amount := big.NewInt(100)

	txHash, moduleErr := module.SupplyNHB("default", user, amount)
	if moduleErr == nil {
		t.Fatalf("expected error when module paused, got tx hash %q", txHash)
	}
	if moduleErr.Message != nativecommon.ErrModulePaused.Error() {
		t.Fatalf("expected ErrModulePaused, got %v", moduleErr.Message)
	}
	if txHash != "" {
		t.Fatalf("expected empty tx hash on failure, got %q", txHash)
	}

	after := snapshotLendingState(t, node, userAddr)
	if after != before {
		t.Fatalf("state mutated while paused\n before: %+v\n after:  %+v", before, after)
	}
}

type lendingSnapshot struct {
	userBalanceNHB     string
	userBalanceZNHB    string
	moduleBalanceNHB   string
	collateralBalance  string
	marketSupplied     string
	marketSupplyShares string
	marketBorrowed     string
	userAccountExists  bool
	userSupplyShares   string
}

func snapshotLendingState(t *testing.T, node *core.Node, addr crypto.Address) lendingSnapshot {
	t.Helper()
	var snap lendingSnapshot
	err := node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(addr.Bytes())
		if err != nil {
			return err
		}
		snap.userBalanceNHB = account.BalanceNHB.String()
		if account.BalanceZNHB != nil {
			snap.userBalanceZNHB = account.BalanceZNHB.String()
		}

		moduleAccount, err := manager.GetAccount(node.LendingModuleAddress().Bytes())
		if err != nil {
			return err
		}
		snap.moduleBalanceNHB = moduleAccount.BalanceNHB.String()

		collateralAccount, err := manager.GetAccount(node.LendingCollateralAddress().Bytes())
		if err != nil {
			return err
		}
		snap.collateralBalance = collateralAccount.BalanceZNHB.String()

		market, ok, err := manager.LendingGetMarket("default")
		if err != nil {
			return err
		}
		if ok && market != nil {
			if market.TotalNHBSupplied != nil {
				snap.marketSupplied = market.TotalNHBSupplied.String()
			}
			if market.TotalSupplyShares != nil {
				snap.marketSupplyShares = market.TotalSupplyShares.String()
			}
			if market.TotalNHBBorrowed != nil {
				snap.marketBorrowed = market.TotalNHBBorrowed.String()
			}
		}

		var userKey [20]byte
		copy(userKey[:], addr.Bytes())
		userAccount, exists, err := manager.LendingGetUserAccount("default", userKey)
		if err != nil {
			return err
		}
		snap.userAccountExists = exists
		if exists && userAccount != nil && userAccount.SupplyShares != nil {
			snap.userSupplyShares = userAccount.SupplyShares.String()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot lending state: %v", err)
	}
	return snap
}

func seedPausedLendingState(node *core.Node, userAddr crypto.Address) error {
	return node.WithState(func(manager *nhbstate.Manager) error {
		userAccount := &types.Account{BalanceNHB: big.NewInt(1000), BalanceZNHB: big.NewInt(0)}
		if err := manager.PutAccount(userAddr.Bytes(), userAccount); err != nil {
			return err
		}

		moduleAddr := node.LendingModuleAddress()
		moduleAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
		if err := manager.PutAccount(moduleAddr.Bytes(), moduleAccount); err != nil {
			return err
		}

		collateralAddr := node.LendingCollateralAddress()
		collateralAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
		if err := manager.PutAccount(collateralAddr.Bytes(), collateralAccount); err != nil {
			return err
		}

		market := &lending.Market{
			PoolID:            "default",
			DeveloperOwner:    userAddr,
			DeveloperFeeBps:   0,
			TotalNHBSupplied:  big.NewInt(0),
			TotalSupplyShares: big.NewInt(0),
			TotalNHBBorrowed:  big.NewInt(0),
		}
		if err := manager.LendingPutMarket("default", market); err != nil {
			return err
		}
		return nil
	})
}
