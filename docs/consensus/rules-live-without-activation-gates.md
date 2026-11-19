# Rules that are live without an activation height

Some rules of this chain changed in a release without a block height from which they apply.
They are not switched by a setting, they cannot be switched off, and a node that executes a
block older than the first release carrying them computes another state than the chain did.
Live validators do not re-execute history, and a new node starts from a snapshot taken after
the release (`docs/validators/snapshot-onboarding.md`), so the practical exposure is the
replay of old blocks, which is not supported.

This page says exactly what is live for each, where the test that pins it is, and what has to
happen when one of them is changed again. It does not propose to gate them retroactively: the
old behaviour of several of them cannot be reproduced, and gating a refusal of self-dealing
would keep the old value-creating path.

**Rule for the next change.** A change to anything listed here is a consensus change. It
ships behind an activation height (`docs/consensus/invariants.md`), default off, and the test
named under the rule is edited in the same commit, on purpose, to state the new rule for the
heights it applies to. A change that makes one of those tests fail without touching it is a
bug, whatever else it fixes.

## PL-R5T-67: POS capture and void ids, and the POS registry rules

Live since release r5 (commits 40821b0 and 8987812 on the line that became the live release),
unchanged since. Nothing here has a setting.

What is live, in `core/state_pos.go`:

* **Capture and void ids.** The authorization id of a `TxTypePOSCapture` (0x21) or
  `TxTypePOSVoid` (0x22) transaction is accepted in every encoding the authorize step can
  report: 64 hexadecimal characters, with or without `0x`. Before r5 the 64-hex id was read as
  text, so no capture or void could ever match a real authorization and none could succeed. A
  malformed id is refused.
  Pinned by `TestPOSCaptureAcceptsEveryAuthorizeIDEncoding`,
  `TestPOSVoidAcceptsEveryAuthorizeIDEncoding` and
  `TestPOSCaptureAndVoidRejectMalformedAuthorizationIDs`.
* **Registry transactions.** A `TxTypePOSRegistry` (0x23) payload must be a packed registry
  message (`google.protobuf.Any` naming one of the six `pos.v1` messages); its signer must own
  the merchant address or hold `ROLE_POS_REGISTRY_ADMIN`; empty bytes, garbage and a packed
  message of another module fail. Before r5 empty, garbage and wrongly wrapped registry
  transactions succeeded as no-ops that only advanced the sender's nonce.
  Pinned by `TestPOSRegistryPayloadMustNameARegistryMessage`,
  `TestPOSRegistryRefusesAnOutsider` and `TestPOSRegistryForgedAuthorityIsRefused`.

What differs from the old rules, so what a replay of blocks from before r5 would get wrong:
a block that contains a successful capture or void, or a registry transaction that was empty,
garbage or wrongly wrapped, is valid under one rule set and rejected or executed differently
under the other.

Evidence: the earlier scan of the first 196,693 blocks found no point-of-sale transaction in
them. The blocks between that height and the r5 deploy have not been scanned.

**Recommendation to the owner: ACCEPTED**, on the condition that the history scan below finds
no transaction of types 0x21, 0x22 or 0x23 in the unscanned range. Old capture and void could
not succeed, and no service of this repository sends registry transactions, so there is
probably nothing to find. If the scan finds some, the decision is whether a node that follows
from genesis is wanted; if it is, a legacy-rules setting with the repository's activation
height pattern (old code below the height) is built then, and only then.

**The one open step** is the same for this item, PL-R5T-86 and PL-R5T-88: a scan of the blocks
between 196,693 and the r5 deploy for transactions of the types these rules touch (0x21, 0x22,
0x23, the escrow arbitration types, evidence submission, subscription and market and lending
transactions), from a copy of a validator data directory or a local node with full history.
Reading thousands of blocks through the production node was not done: the portal shares the
node's loopback rate-limit allowance.

## PL-R5T-86: transfer fee crediting when the fee policy is disabled

Live since the commit "Fix NHB transfer fee being destroyed when the transfer-gas policy is
disabled" (c9e89e8, ledger PL-DC-08), which is in the live release (a649ca5) and in every
release after it. Nothing here has a setting.

What is live, in `core/state_transition.go` (the NHB transfer path): when a fee was debited
from the sender it is credited to the transfer-gas fee collector whether or not the
transfer-gas policy is `Enabled`, for the sender's own fee and for a sponsored transfer, and
when the collector is the transfer's sender or recipient the credit goes to that account's
own object. `Enabled` decides only whether the free-tier eligibility check runs. A transfer
whose collector is the zero address and whose fee is positive fails with `fees: transfer gas
collector not configured`. Pinned by `TestTransferGasPolicyDisabledStillCreditsFee`.

Before that commit the credit ran only when `Enabled`, so a policy with `Enabled = false` and
`FeeBps > 0` burned the fee. That combination needs the node config to set
`TransferFreeTierSpendWei` to zero or empty while `TransferFeeBps` is positive (the loader
turns `Enabled` off when the allowance is not positive). The shipped defaults (an allowance
of 1,000 NHB, 20 bps) keep `Enabled = true`, and under them both rule sets give the same
result for every transfer; a block in the old range under the other combination would be the
one that differs.

**Recommendation to the owner: ACCEPTED.** The change is already live, the shipped
configuration is unaffected by it, and the ledger's own condition for closing it is a
confirmation that the validator hosts do not set `TransferFreeTierSpendWei` to zero or empty
with a positive `TransferFeeBps`, and that no transfer in history named a paymaster under that
combination. The first half is a read of the two hosts' config; the second is the same history
scan as PL-R5T-67. If the config check finds the combination, the ledger's alternative is a
height gate at the two credit sites in a coordinated deploy, which would be built then; it is
not built now because the credit is already live and a gate for the old (burning) behaviour
would keep a path that destroys users' money.

## PL-R5T-88: value-engine rules (arbitration nonce, self-dealing refusals, block-time stamps, signed evidence)

Live since release r5 (commits 04d7201, 45ba339, e36b9c2 and e39ff1b on the line that became
the live release), unchanged since. Nothing here has a setting. The snapshot onboarding page
(`docs/validators/snapshot-onboarding.md`) already lists them as ungated since r5, beside the
treasury booking, and says the only way to use a node on this chain is a post-r5 snapshot.

What is live, and the test that pins each:

* **Arbitration consumes the relayer's nonce** (escrow arbitration, types 0x0B and 0x0C):
  `TestArbitrationAdvancesTheRelayersNonce`, `TestFailedArbitrationLeavesTheRelayersNonce`.
* **Self-dealing is refused**, and each engine uses one account object per address: an escrow,
  market fill, subscription or liquidation in which one account fills two roles fails.
  `TestApplyMarketFillListing_SelfFillIsRefusedAndConservesValue`,
  `TestSubscriptionSelfSubscribeIsRejectedAndNeverMints`,
  `TestApplyLendingLiquidate_SelfLiquidationIsPrunedAndMovesNothing`.
* **Escrow and trade timestamps are the block time**, not the host clock:
  `TestEscrowTransactionsAreWallClockIndependent` asserts an escrow created in a block carries
  the block time and that two processors with different wall clocks reach the same state root.
* **Evidence submission (type 0x4C) is signed**, carries a nonce and needs bonded stake:
  `TestSubmitEvidenceReporterMustBeSigner` and the evidence tests of `core/potso_evidence_*_test.go`.

Why they are not gated: the old wall-clock stamps cannot be reproduced (a clock is not in the
block), and gating the self-dealing refusals would keep the old value-creating paths. Live
validators do not re-execute history, so the exposure is the replay of blocks older than the
first release carrying them, which is already unsupported.

The ledger's four steps: (1) list them as ungated since r5 in the snapshot onboarding page,
done; (2) a test that an escrow made in a block carries the block time, done (the test above);
(3) the owner confirms no block before r5 holds an affected transaction, **not done**; (4) bring
public main to parity by pushing the line, which is the public-push decision recorded as
PL-JN-08 and is not a code step. Step 3 is the scan named under PL-R5T-67: the first 196,693
blocks hold no escrow, market, subscription, point-of-sale, voucher or evidence transaction;
the range from there to the r5 deploy is unscanned.

**Recommendation to the owner: ACCEPTED**, and the item closes when the scan finds none. If it
finds some and a node that follows from genesis is wanted, a height-gated legacy release is the
only way to get one, and it is a separate piece of work (a release that is inert above the
current tip), not a change to these rules.
