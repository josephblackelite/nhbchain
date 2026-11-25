package core

import (
	"errors"
	"strings"

	"nhbchain/config"
)

// ErrTransferFeeCollectorUnset is returned by CheckTransferFeeCollector.
var ErrTransferFeeCollectorUnset = errors.New("transfer fee collector is not set: the loaded genesis names no admin wallet, NHB_MASTER_TREASURY is not set and [global.Fees] TransferFeeCollector is empty, so this node would credit the transfer fee to its own validator address and two such nodes would disagree on the state root; set TransferFeeCollector (the same value on every validator), name an admin wallet in the genesis, or set TransferFeeBps and TransferFeeBpsZNHB to 0")

// CheckTransferFeeCollector refuses a configuration under which the transfer fee would be credited
// to an account that depends on which node is running (ledger PL-EXT-73).
//
// The fee collector of the transfer-gas policy is the node's treasury unless TransferFeeCollector
// is set (buildTransferGasPolicyFromConfig), and the treasury is the genesis admin wallet (or the
// NHB_MASTER_TREASURY override) when there is one and the node's OWN validator address when there
// is not. Two validators on a genesis without an admin wallet and without a configured collector
// therefore credit the same fee to two different accounts and their state roots diverge on the
// first fee-paying transfer. The live network's genesis names an admin wallet, so this check never
// fires there; it exists so that a private network started from a genesis without one fails at
// start-up with a message that says what to set, instead of halting at the first fee.
//
// It is not a consensus rule and changes no block: it only decides whether a process starts. A fee
// of 0 on both assets credits nothing, so that combination is accepted. An invalid fee
// configuration is not this check's to report (SetGlobalConfig already does), so it returns nil.
func CheckTransferFeeCollector(fees config.Fees, hasAdminWallet bool) error {
	if hasAdminWallet {
		return nil
	}
	if strings.TrimSpace(fees.TransferFeeCollector) != "" {
		return nil
	}
	if fees.TransferFeeBps == 0 && fees.TransferFeeBpsZNHB == 0 {
		return nil
	}
	return ErrTransferFeeCollectorUnset
}

// CheckTransferFeeCollector applies the package check to this node: whether its treasury is a real
// admin wallet is known from NewNode. Call it once after SetGlobalConfig at start-up.
func (n *Node) CheckTransferFeeCollector(fees config.Fees) error {
	if n == nil {
		return nil
	}
	return CheckTransferFeeCollector(fees, n.adminWalletResolved)
}
