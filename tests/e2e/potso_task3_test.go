package e2e

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/consensus/potso/penalty"
	"nhbchain/consensus/potso/rewards"
	statebank "nhbchain/state/bank"
	statepotso "nhbchain/state/potso"
	"nhbchain/storage"
)

type operationPlan struct {
	Evidence evidence.Evidence
	Hash     [32]byte
	Attempts []attemptPlan
}

type attemptPlan struct {
	ReceivedAt int64
	Context    penalty.Context
}

type epochPlan struct {
	Number        uint64
	Pool          *big.Int
	NodeAAttempts []int
	NodeBAttempts []int
	NodeAOrder    []int
	NodeBOrder    []int
}

type weightSeed struct {
	Base    *big.Int
	Current *big.Int
}

type opInstance struct {
	Operation int
	Attempt   int
}

type scenarioPlan struct {
	Floor         *big.Int
	Ceiling       *big.Int
	Participants  [][20]byte
	Initial       map[[20]byte]weightSeed
	Operations    []operationPlan
	Epochs        []epochPlan
	NodeASequence []opInstance
	NodeBSequence []opInstance
}

type pipelineSnapshot struct {
	Weights    map[string]string            `json:"weights"`
	Rewards    map[string]map[string]string `json:"rewards"`
	Totals     map[string]string            `json:"totals"`
	Dust       string                       `json:"dust"`
	Deliveries map[string]deliverySnapshot  `json:"deliveries"`
}

type deliverySnapshot struct {
	Address string `json:"address"`
	Amount  string `json:"amount"`
}

type deliveryRecord struct {
	Epoch    uint64
	Address  string
	Amount   string
	Attempts int
}

type pipelineNode struct {
	name         string
	db           storage.Database
	ledger       *statepotso.Ledger
	penalty      *penalty.Engine
	rewards      *rewards.Ledger
	rounding     *rewards.RoundingBucket
	exporter     *mockExporter
	participants [][20]byte
	totals       map[uint64]*big.Int
	epochDust    map[uint64]*big.Int
	records      map[[32]byte]*evidence.Record
}

type mockExporter struct {
	deliveries   map[string]deliveryRecord
	hadDuplicate bool
}

func TestPotsoTask3Determinism(t *testing.T) {
	seeds := []int64{42, 1337, 9001}
	updateGolden := os.Getenv("UPDATE_POTSO_GOLDEN") == "1"

	for _, seed := range seeds {
		plan, err := buildScenarioPlan(seed)
		if err != nil {
			t.Fatalf("seed %d: build plan: %v", seed, err)
		}
		nodeA, err := newPipelineNode("nodeA", plan)
		if err != nil {
			t.Fatalf("seed %d: nodeA init: %v", seed, err)
		}
		defer nodeA.close()
		snapshotA, err := executePlan(nodeA, plan, plan.NodeASequence, func(ep epochPlan) []int { return ep.NodeAAttempts }, func(ep epochPlan) []int { return ep.NodeAOrder })
		if err != nil {
			t.Fatalf("seed %d: execute nodeA: %v", seed, err)
		}
		nodeB, err := newPipelineNode("nodeB", plan)
		if err != nil {
			t.Fatalf("seed %d: nodeB init: %v", seed, err)
		}
		defer nodeB.close()
		snapshotB, err := executePlan(nodeB, plan, plan.NodeBSequence, func(ep epochPlan) []int { return ep.NodeBAttempts }, func(ep epochPlan) []int { return ep.NodeBOrder })
		if err != nil {
			t.Fatalf("seed %d: execute nodeB: %v", seed, err)
		}
		if !reflect.DeepEqual(snapshotA, snapshotB) {
			aJSON, _ := json.Marshal(snapshotA)
			bJSON, _ := json.Marshal(snapshotB)
			t.Fatalf("seed %d: node snapshots diverged\nA=%s\nB=%s", seed, string(aJSON), string(bJSON))
		}
		if !nodeA.exporter.hadDuplicate && !nodeB.exporter.hadDuplicate {
			t.Fatalf("seed %d: expected duplicate deliveries to be exercised", seed)
		}
		if err := compareWithGolden(seed, snapshotA, updateGolden); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
	}
}

func buildScenarioPlan(seed int64) (scenarioPlan, error) {
	rng := rand.New(rand.NewSource(seed))
	participantCount := 4 + rng.Intn(3)
	participants := make([][20]byte, participantCount)
	seen := make(map[[20]byte]struct{}, participantCount)
	for i := range participants {
		var addr [20]byte
		for {
			for j := range addr {
				addr[j] = byte(rng.Intn(256))
			}
			if _, ok := seen[addr]; !ok {
				seen[addr] = struct{}{}
				break
			}
		}
		participants[i] = addr
	}
	floor := big.NewInt(int64(100 + rng.Intn(50)))
	ceil := big.NewInt(int64(700 + rng.Intn(500)))
	if ceil.Cmp(floor) <= 0 {
		ceil = new(big.Int).Add(floor, big.NewInt(100))
	}
	initial := make(map[[20]byte]weightSeed, len(participants))
	for _, addr := range participants {
		base := big.NewInt(int64(500 + rng.Intn(400)))
		current := new(big.Int).Sub(base, big.NewInt(int64(rng.Intn(150))))
		if current.Cmp(floor) < 0 {
			current = new(big.Int).Set(floor)
		}
		initial[addr] = weightSeed{Base: base, Current: current}
	}
	evidenceTypes := []evidence.Type{evidence.TypeDowntime, evidence.TypeEquivocation, evidence.TypeInvalidBlockProposal}
	const baseTimestamp = int64(1_700_000_000)
	opCount := 6 + rng.Intn(4)
	operations := make([]operationPlan, opCount)
	for i := 0; i < opCount; i++ {
		typ := evidenceTypes[rng.Intn(len(evidenceTypes))]
		offender := participants[rng.Intn(len(participants))]
		heights := make([]uint64, 1+rng.Intn(3))
		baseHeight := uint64(50 + rng.Intn(200))
		for j := range heights {
			heights[j] = baseHeight + uint64(rng.Intn(30))
		}
		details := make([]byte, 4)
		for j := range details {
			details[j] = byte(rng.Intn(256))
		}
		reporter := participants[rng.Intn(len(participants))]
		evidence := evidence.Evidence{
			Type:        typ,
			Offender:    offender,
			Heights:     heights,
			Details:     append([]byte(nil), details...),
			Reporter:    reporter,
			ReporterSig: []byte{byte(i), byte(i + 1), byte(i + 2)},
			Timestamp:   baseTimestamp + int64(rng.Intn(7200)),
		}
		hash, err := evidence.CanonicalHash()
		if err != nil {
			return scenarioPlan{}, err
		}
		attemptCount := 1 + rng.Intn(3)
		attempts := make([]attemptPlan, attemptCount)
		ctx := penalty.Context{
			BlockHeight:  uint64(100 + rng.Intn(900)),
			MissedEpochs: uint64(rng.Intn(4)),
		}
		if rng.Intn(4) == 0 {
			ctx.BaseWeightOverride = big.NewInt(int64(400 + rng.Intn(200)))
		}
		for j := range attempts {
			attempts[j] = attemptPlan{
				ReceivedAt: baseTimestamp + int64(rng.Intn(7200)) + int64(j*5),
				Context:    ctx,
			}
		}
		operations[i] = operationPlan{Evidence: evidence, Hash: hash, Attempts: attempts}
	}
	duplicate := false
	for _, op := range operations {
		if len(op.Attempts) > 1 {
			duplicate = true
			break
		}
	}
	if !duplicate {
		op := &operations[0]
		if len(op.Attempts) > 0 {
			op.Attempts = append(op.Attempts, op.Attempts[0])
		} else {
			op.Attempts = []attemptPlan{{ReceivedAt: baseTimestamp + 10, Context: penalty.Context{BlockHeight: 10}}}
		}
	}
	epochCount := 3
	epochs := make([]epochPlan, epochCount)
	for i := 0; i < epochCount; i++ {
		pool := big.NewInt(int64(2000 + rng.Intn(2000)))
		attemptsA := make([]int, len(participants))
		attemptsB := make([]int, len(participants))
		orderA := make([]int, len(participants))
		orderB := make([]int, len(participants))
		for j := range participants {
			attemptsA[j] = 1 + rng.Intn(3)
			attemptsB[j] = 1 + rng.Intn(3)
			orderA[j] = j
			orderB[j] = j
		}
		if attemptsA[0] < 2 {
			attemptsA[0] = 2
		}
		if attemptsB[0] < 2 {
			attemptsB[0] = 2
		}
		shuffle := rand.New(rand.NewSource(seed + int64(i)*97 + 12345))
		shuffle.Shuffle(len(orderB), func(a, b int) { orderB[a], orderB[b] = orderB[b], orderB[a] })
		epochs[i] = epochPlan{
			Number:        uint64(i + 1),
			Pool:          pool,
			NodeAAttempts: attemptsA,
			NodeBAttempts: attemptsB,
			NodeAOrder:    append([]int(nil), orderA...),
			NodeBOrder:    append([]int(nil), orderB...),
		}
	}
	instances := make([]opInstance, 0)
	for i, op := range operations {
		for j := range op.Attempts {
			instances = append(instances, opInstance{Operation: i, Attempt: j})
		}
	}
	nodeA := append([]opInstance(nil), instances...)
	sort.Slice(nodeA, func(i, j int) bool {
		ai := operations[nodeA[i].Operation].Attempts[nodeA[i].Attempt].ReceivedAt
		aj := operations[nodeA[j].Operation].Attempts[nodeA[j].Attempt].ReceivedAt
		if ai == aj {
			return nodeA[i].Operation < nodeA[j].Operation
		}
		return ai < aj
	})
	nodeB := append([]opInstance(nil), instances...)
	shuffleOps := rand.New(rand.NewSource(seed ^ 0x517cc1b727220a95))
	shuffleOps.Shuffle(len(nodeB), func(i, j int) { nodeB[i], nodeB[j] = nodeB[j], nodeB[i] })
	return scenarioPlan{
		Floor:         floor,
		Ceiling:       ceil,
		Participants:  participants,
		Initial:       initial,
		Operations:    operations,
		Epochs:        epochs,
		NodeASequence: nodeA,
		NodeBSequence: nodeB,
	}, nil
}

func newPipelineNode(name string, plan scenarioPlan) (*pipelineNode, error) {
	db := storage.NewMemDB()
	ledger, err := statepotso.NewLedger(plan.Floor, plan.Ceiling)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	for addr, seed := range plan.Initial {
		if _, err := ledger.Set(addr, seed.Base, seed.Current); err != nil {
			return nil, fmt.Errorf("ledger seed: %w", err)
		}
	}
	catalog, err := penalty.BuildCatalog(penalty.DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	engine := penalty.NewEngine(catalog, ledger, statebank.NewNoopSlasher(false))
	node := &pipelineNode{
		name:         name,
		db:           db,
		ledger:       ledger,
		penalty:      engine,
		rewards:      rewards.NewLedger(db),
		rounding:     rewards.NewRoundingBucket(),
		exporter:     newMockExporter(),
		participants: append([][20]byte(nil), plan.Participants...),
		totals:       make(map[uint64]*big.Int),
		epochDust:    make(map[uint64]*big.Int),
		records:      make(map[[32]byte]*evidence.Record),
	}
	return node, nil
}

func (n *pipelineNode) ingest(op operationPlan, attempt attemptPlan) error {
	if _, exists := n.records[op.Hash]; exists {
		return nil
	}
	record := &evidence.Record{Hash: op.Hash, Evidence: op.Evidence.Clone(), ReceivedAt: attempt.ReceivedAt}
	n.records[op.Hash] = record
	res, err := n.penalty.Apply(record, attempt.Context)
	if err != nil {
		return fmt.Errorf("penalty apply: %w", err)
	}
	if res == nil {
		return fmt.Errorf("penalty result nil")
	}
	return nil
}

func (n *pipelineNode) settleEpoch(plan epochPlan, attempts []int, order []int) error {
	weights := make([]rewards.WeightEntry, len(n.participants))
	for i, addr := range n.participants {
		entry := n.ledger.Entry(addr)
		weights[i] = rewards.WeightEntry{Address: addr, Weight: entry.Value}
	}
	carry := n.rounding.Balance()
	dist, err := rewards.SplitRewards(plan.Pool, weights, n.rounding)
	if err != nil {
		return fmt.Errorf("split rewards: %w", err)
	}
	allowance := new(big.Int).Add(plan.Pool, carry)
	if dist.TotalAssigned.Cmp(allowance) > 0 {
		return fmt.Errorf("assigned %s exceeds allowance %s", dist.TotalAssigned, allowance)
	}
	entries := make([]*rewards.RewardEntry, len(dist.Shares))
	for i, share := range dist.Shares {
		entries[i] = &rewards.RewardEntry{
			Epoch:    plan.Number,
			Address:  share.Address,
			Amount:   new(big.Int).Set(share.Amount),
			Currency: "ZNHB",
		}
		entries[i].Checksum = rewards.EntryChecksum(plan.Number, share.Address, share.Amount)
	}
	if err := n.rewards.PutBatch(entries); err != nil {
		return fmt.Errorf("rewards ledger: %w", err)
	}
	n.totals[plan.Number] = new(big.Int).Set(dist.TotalAssigned)
	n.epochDust[plan.Number] = new(big.Int).Set(dist.Dust)
	if err := n.exporter.Deliver(entries, order, attempts); err != nil {
		return err
	}
	return nil
}

func (n *pipelineNode) snapshot() (pipelineSnapshot, error) {
	weights := make(map[string]string, len(n.participants))
	for _, addr := range n.participants {
		entry := n.ledger.Entry(addr)
		weights[hex.EncodeToString(addr[:])] = entry.Value.String()
	}
	rewardsMap := map[string]map[string]string{}
	entries, _, err := n.rewards.List(rewards.RewardFilter{})
	if err != nil {
		return pipelineSnapshot{}, fmt.Errorf("list rewards: %w", err)
	}
	for _, entry := range entries {
		epochKey := fmt.Sprintf("%d", entry.Epoch)
		bucket, ok := rewardsMap[epochKey]
		if !ok {
			bucket = map[string]string{}
			rewardsMap[epochKey] = bucket
		}
		amount := "0"
		if entry.Amount != nil {
			amount = entry.Amount.String()
		}
		bucket[hex.EncodeToString(entry.Address[:])] = amount
	}
	totals := make(map[string]string, len(n.totals))
	for epoch, total := range n.totals {
		totals[fmt.Sprintf("%d", epoch)] = total.String()
	}
	snapshot := pipelineSnapshot{
		Weights:    weights,
		Rewards:    rewardsMap,
		Totals:     totals,
		Dust:       n.rounding.Balance().String(),
		Deliveries: n.exporter.snapshot(),
	}
	return snapshot, nil
}

func (n *pipelineNode) close() {
	if n == nil || n.db == nil {
		return
	}
	n.db.Close()
}

func executePlan(node *pipelineNode, plan scenarioPlan, sequence []opInstance, attemptSel func(epochPlan) []int, orderSel func(epochPlan) []int) (pipelineSnapshot, error) {
	for _, inst := range sequence {
		op := plan.Operations[inst.Operation]
		if inst.Attempt >= len(op.Attempts) {
			return pipelineSnapshot{}, fmt.Errorf("invalid attempt index")
		}
		if err := node.ingest(op, op.Attempts[inst.Attempt]); err != nil {
			return pipelineSnapshot{}, err
		}
	}
	for _, epoch := range plan.Epochs {
		attempts := append([]int(nil), attemptSel(epoch)...)
		order := append([]int(nil), orderSel(epoch)...)
		if len(order) != len(node.participants) {
			return pipelineSnapshot{}, fmt.Errorf("invalid order length")
		}
		if err := node.settleEpoch(epoch, attempts, order); err != nil {
			return pipelineSnapshot{}, err
		}
	}
	return node.snapshot()
}

func newMockExporter() *mockExporter {
	return &mockExporter{deliveries: map[string]deliveryRecord{}}
}

func (m *mockExporter) Deliver(entries []*rewards.RewardEntry, order []int, attempts []int) error {
	for _, idx := range order {
		if idx < 0 || idx >= len(entries) {
			return fmt.Errorf("invalid delivery order index %d", idx)
		}
		entry := entries[idx]
		count := 1
		if idx < len(attempts) && attempts[idx] > 0 {
			count = attempts[idx]
		}
		if count > 1 {
			m.hadDuplicate = true
		}
		for i := 0; i < count; i++ {
			if err := m.record(entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *mockExporter) record(entry *rewards.RewardEntry) error {
	if entry == nil {
		return fmt.Errorf("nil reward entry")
	}
	checksum := entry.Checksum
	if checksum == "" {
		checksum = rewards.EntryChecksum(entry.Epoch, entry.Address, entry.Amount)
	}
	addr := hex.EncodeToString(entry.Address[:])
	amount := "0"
	if entry.Amount != nil {
		amount = entry.Amount.String()
	}
	rec, ok := m.deliveries[checksum]
	if ok {
		if rec.Amount != amount || rec.Address != addr {
			return fmt.Errorf("inconsistent delivery for %s", checksum)
		}
	} else {
		rec = deliveryRecord{Epoch: entry.Epoch, Address: addr, Amount: amount}
	}
	rec.Attempts++
	m.deliveries[checksum] = rec
	return nil
}

func (m *mockExporter) snapshot() map[string]deliverySnapshot {
	out := make(map[string]deliverySnapshot, len(m.deliveries))
	for checksum, rec := range m.deliveries {
		out[checksum] = deliverySnapshot{Address: rec.Address, Amount: rec.Amount}
	}
	return out
}

func compareWithGolden(seed int64, snapshot pipelineSnapshot, update bool) error {
	path, err := goldenFilePath(seed)
	if err != nil {
		return err
	}
	if update {
		if err := writeGolden(path, snapshot); err != nil {
			return fmt.Errorf("write golden: %w", err)
		}
		return nil
	}
	expected, err := readGolden(path)
	if err != nil {
		return fmt.Errorf("read golden: %w", err)
	}
	if !reflect.DeepEqual(expected, snapshot) {
		expectedJSON, _ := json.MarshalIndent(expected, "", "  ")
		actualJSON, _ := json.MarshalIndent(snapshot, "", "  ")
		return fmt.Errorf("snapshot mismatch\nexpected:\n%s\nactual:\n%s", string(expectedJSON), string(actualJSON))
	}
	return nil
}

func goldenFilePath(seed int64) (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime caller lookup failed")
	}
	base := filepath.Join(filepath.Dir(filepath.Dir(file)), "golden", "potso")
	return filepath.Join(base, fmt.Sprintf("seed_%04d.json", seed)), nil
}

func writeGolden(path string, snapshot pipelineSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readGolden(path string) (pipelineSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pipelineSnapshot{}, err
	}
	var snapshot pipelineSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return pipelineSnapshot{}, err
	}
	return snapshot, nil
}
