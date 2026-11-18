# Validator set, quorum and liveness: what is guaranteed and what is not

Written with the admission guard (`ValidatorAdmissionGuardActivationHeight`, ledger
PL-VAL-01) and records the decisions on PL-VAL-02 and what remains of PL-R5T-07. Code
references are to this tree.

## The arithmetic

A block commits when strictly more than two thirds of the total voting power has
precommitted it (`types.HasQuorum`, `core/types/quorum.go`): `floor(2 x total / 3) + 1`.
A validator's voting power is its entry in the validator set, which is its staking
`Stake` including everything delegated to it (`validatorEligibilityBasis`), refreshed at
each epoch boundary (`applyValidatorSelection`, 100 blocks by default).

Consequences that hold for any set:

- With two validators of equal power both are needed. Two validators have no fault
  tolerance; a set needs four equal validators to survive the loss of one.
- Any group of validators holding a third of the power or more can stop the chain by
  staying silent. Any group holding more than two thirds can commit alone.
- The set only changes when blocks are produced (epoch boundaries). When blocks stop,
  nothing on the chain, including a transaction that would remove or unstake the
  offending validator, can run. The only repair is the operator recovery primitive
  (`RemoveValidatorFromSet` / `ReplaceValidatorSet`, `core/node.go`) applied by hand to
  every validator.

## PL-VAL-01: joining must not be able to stop the chain

Without a guard a validator that registers and holds the minimum stake enters the set at
the next boundary. If the validators already in it sit close to a third each, the newcomer
is needed for the quorum, and if it goes away no block can be finished.

Two defences, and they are independent:

1. **Stake route (an operator action, no code).** Put far more stake on the established
   validators than any plausible group of newcomers will hold. If each established
   validator carries `S` and all other validators together carry less than `S`, the
   established ones hold more than two thirds between them, so newcomers can neither stall
   the chain nor be needed for it. An attacker then needs about `S` in locked, slashable
   stake. This is what raised the price of the attack from the minimum stake to the order
   of 10,000,000 ZNHB on the live set.
2. **Admission guard (code, default off).** From `ValidatorAdmissionGuardActivationHeight`
   (with `ValidatorAdmissionProbationSeconds` above zero), at each epoch boundary a
   candidate that is not in the set and has not been in it for the whole probation is
   admitted only while the established validators hold strictly more than twice the power
   of the newcomers. The newcomers together stay below a third of the set, and the
   established validators keep their quorum without them whatever the newcomers do.
   Details and the exact rule: `docs/consensus/invariants.md`, `core/validator_admission.go`.

What the guard costs, stated plainly:

- **A large honest validator cannot join.** If the established validators hold `S`, newcomers
  as a group stay below `S / 2`. With two established validators at 10,010,000 ZNHB each, a
  newcomer of 10,010,000 or more is deferred. It stays eligible and is considered at every
  boundary; it is admitted when the budget allows. A newcomer admitted below the cap
  becomes established after the probation, which raises `S` and the budget. Growth is
  therefore geometric and slow, by design. Who may join at what size is a policy decision
  (owner), and so are the probation length and the order newcomers are served in
  (registration time, then address).
- **It does not give fault tolerance.** With two established validators the loss of either
  still stops the chain. Fault tolerance needs four or more established validators of
  comparable power (infrastructure and cost, ledger PL-IN-D6-15).
- **It protects the quorum, not the proposer rotation or the rewards.** A deferred
  newcomer earns nothing as a validator until admitted.

## PL-VAL-02: blocks of 4 seconds instead of 2 while one validator holds more than two thirds

Observed once on the live set: for about 13 minutes, while one validator's stake had been
changed an epoch before the other's, blocks took 4 seconds and the large validator
proposed nearly every block. Nothing was lost; payments confirmed in 4 seconds instead of
2.

**Cause (read from `consensus/bft/bft.go`, `runRound`).** A validator holding more than two
thirds of the power alone reaches the prevote and precommit quorum inside its own
`prevote()` and `precommit()` calls and commits inline. The round loop is event driven: it
leaves only when a timer fires, an external commit arrives, or `committedBlocks[height]` is
checked after some event. With no vote from the other validator needed, no event arrives
and the round ends on the commit timer (`defaultCommitTimeout`, 4 seconds). When the other
validator's precommit does arrive the round ends on it, a few milliseconds after the
commit, and blocks come at the minimum block interval.

**Decision: no code change in this release.**

- It is a delay of the block cadence, not a safety or liveness fault: every height was
  decided in the first round, the two nodes stayed in step, and no vote was lost.
- It occurs only when one validator holds more than two thirds by itself. With the
  admission guard on, that cannot come from a newcomer; it can still come from a stake
  change on one established validator.
- The avoidance is operational and sufficient: change the stake of all established
  validators in the same epoch (100 blocks, a few minutes), as PL-VAL-01 already asked.
- A code fix is possible and is a node-local liveness change, not a protocol rule (no
  message, signature, vote, lock or block-validity rule changes): after an inline
  `prevote()` or `precommit()`, check `committedBlocks[height]` before blocking in the
  loop. It changes when both validators start the next height, needs both restarted
  together and a multi-validator proof (equal and lopsided powers, restarts) first. It is
  worth doing only if lopsided powers become normal.

## PL-R5T-07: round-jump rule when groups are rounds apart

What is in this tree (the live build has the first rule since r7; the second is r11's and is
in r12, not deployed):

1. A validator follows a later round once the validators seen there hold at least a third of
   the power (`supportedRoundLocked`, `consensus/bft/round_sync.go`). This replaces the strict
   rule that needed more than a third AND, with the validator's own power, a quorum, which
   could leave two groups rounds apart that never meet (3 equal validators with 1 ahead; 2
   validators staked 2:1; 5 equal with 2 ahead).
2. A validator also follows a group below a third when no quorum can be made without it,
   bounded to eight distinct rounds per height, with the budget kept across a restart in
   `bft_weak_jump.json` in the data directory (`consensus/bft/weak_jump_snapshot.go`). This
   is the restart-wedge fix (ledger PL-R1-RESTARTWEDGE). The two compose: the third-or-more
   rule is checked first.

What is proven: deterministic tables for both rules including unequal powers; real-timer
tests for split groups; the restart rig (real engines in one process, random restarts) wedged
10 of 12 runs with four validators and one down, and 12 of 12 with seven and two down, before
rule 2 and 0 of 60 each after.

What is NOT proven, and stays open:

- A real multi-process network of four or more validators, with unequal powers, restarted at
  random. Nobody runs one yet; until the network has four or more validators neither rule
  changes anything in practice (two validators never showed the wedge).
- The trade-off of rule 1: a coalition of exactly a third can move honest validators to a
  round of its choosing. A round move is not a vote and rule 2's budget bounds the other
  direction, but a coalition holding a third can already stop the chain by staying silent.

The localnet evidence added with this release (three real processes, equal and lopsided
powers, a joiner, a hard kill and a restart) is in the W3 report, not here; it does not
cover four or more validators.

## Restart procedure (unchanged)

With more than two validators restart them together, never one by one while the set is at its
minimum quorum; both live validators are restarted in the same second. The engine writes
`bft_weak_jump.json` in its data directory (atomically, like the vote record), so the
directory must stay writable by the service user.
