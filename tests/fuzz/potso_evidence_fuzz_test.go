package fuzz

import (
	"encoding/binary"
	"math/big"
	"math/rand"
	"testing"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/consensus/potso/penalty"
	"nhbchain/consensus/potso/rewards"
	statebank "nhbchain/state/bank"
	statepotso "nhbchain/state/potso"
	"nhbchain/storage"
)

func FuzzPotsoEvidencePipeline(f *testing.F) {
	f.Add([]byte{0x01, 0x02, 0x03, 0x04, 0x05})
	f.Add([]byte{0x10, 0x42, 0xAA, 0xBB, 0xCC, 0xDD})
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) == 0 {
			payload = []byte{0}
		}
		seed := int64(binary.LittleEndian.Uint64(padPayload(payload)))
		rng := rand.New(rand.NewSource(seed))
		db := storage.NewMemDB()
		defer db.Close()
		floor := big.NewInt(100)
		ceil := big.NewInt(5000)
		ledger, err := statepotso.NewLedger(floor, ceil)
		if err != nil {
			t.Fatalf("ledger init: %v", err)
		}
		participants := make([][20]byte, 4)
		for i := range participants {
			for j := range participants[i] {
				participants[i][j] = byte(rng.Intn(256))
			}
			base := big.NewInt(1000 + int64(rng.Intn(2000)))
			current := new(big.Int).Sub(base, big.NewInt(int64(rng.Intn(400))))
			if current.Cmp(floor) < 0 {
				current = new(big.Int).Set(floor)
			}
			if _, err := ledger.Set(participants[i], base, current); err != nil {
				t.Fatalf("seed ledger: %v", err)
			}
		}
		catalog, err := penalty.BuildCatalog(penalty.DefaultConfig())
		if err != nil {
			t.Fatalf("catalog init: %v", err)
		}
		engine := penalty.NewEngine(catalog, ledger, statebank.NewNoopSlasher(false))
		store := evidence.NewStore(db)
		bucket := rewards.NewRoundingBucket()
		rewardLedger := rewards.NewLedger(db)
		evidenceTypes := []evidence.Type{evidence.TypeDowntime, evidence.TypeEquivocation, evidence.TypeInvalidBlockProposal}
		accepted := make(map[[32]byte]struct{})
		const baseTimestamp = int64(1_700_000_000)
		for offset := 0; offset < len(payload); offset += 6 {
			chunk := payload[offset:]
			selector := chunk[0]
			typ := evidenceTypes[int(selector)%len(evidenceTypes)]
			offender := participants[int(selector)%len(participants)]
			reporter := participants[int(chunk[len(chunk)-1])%len(participants)]
			heights := make([]uint64, 1+int(selector%3))
			heightSeed := uint64(10 + int(selector%23))
			for i := range heights {
				idx := (offset + i + 1) % len(payload)
				heights[i] = heightSeed + uint64(payload[idx]%31)
			}
			details := []byte{selector, byte(len(heights)), payload[offset%len(payload)]}
			ev := evidence.Evidence{
				Type:        typ,
				Offender:    offender,
				Heights:     heights,
				Details:     append([]byte(nil), details...),
				Reporter:    reporter,
				ReporterSig: []byte{selector, byte(offset)},
				Timestamp:   baseTimestamp + int64(offset),
			}
			hash, err := ev.CanonicalHash()
			if err != nil {
				t.Fatalf("canonical hash: %v", err)
			}
			record, fresh, err := store.Put(hash, ev, baseTimestamp+int64(offset))
			if err != nil {
				t.Fatalf("store put: %v", err)
			}
			if !fresh {
				if _, ok := accepted[hash]; !ok {
					t.Fatalf("duplicate returned before acceptance")
				}
				continue
			}
			accepted[hash] = struct{}{}
			ctx := penalty.Context{
				BlockHeight:  uint64(100 + offset),
				MissedEpochs: uint64(selector % 4),
			}
			if selector%5 == 0 {
				ctx.BaseWeightOverride = big.NewInt(800 + int64(selector%7)*50)
			}
			if _, err := engine.Apply(record, ctx); err != nil {
				t.Fatalf("penalty apply: %v", err)
			}
		}
		weights := make([]rewards.WeightEntry, len(participants))
		for i, addr := range participants {
			entry := ledger.Entry(addr)
			weights[i] = rewards.WeightEntry{Address: addr, Weight: entry.Value}
		}
		pools := []*big.Int{big.NewInt(6000), big.NewInt(4500)}
		for epoch := range pools {
			dist, err := rewards.SplitRewards(pools[epoch], weights, bucket)
			if err != nil {
				t.Fatalf("split rewards: %v", err)
			}
			if dist.TotalAssigned.Cmp(pools[epoch]) > 0 {
				t.Fatalf("epoch %d over-mint: assigned=%s pool=%s", epoch, dist.TotalAssigned, pools[epoch])
			}
			entries := make([]*rewards.RewardEntry, len(dist.Shares))
			for i, share := range dist.Shares {
				entries[i] = &rewards.RewardEntry{
					Epoch:   uint64(epoch + 1),
					Address: share.Address,
					Amount:  new(big.Int).Set(share.Amount),
				}
				if entries[i].Amount.Sign() < 0 {
					t.Fatalf("negative reward amount")
				}
				entries[i].Checksum = rewards.EntryChecksum(uint64(epoch+1), share.Address, share.Amount)
			}
			if err := rewardLedger.PutBatch(entries); err != nil {
				t.Fatalf("ledger put batch: %v", err)
			}
		}
		if bucket.Balance().Sign() < 0 {
			t.Fatalf("bucket balance negative")
		}
		// ensure ledger outputs are retrievable and non-negative
		for _, addr := range participants {
			entry := ledger.Entry(addr)
			if entry.Value.Sign() < 0 {
				t.Fatalf("negative ledger weight")
			}
		}
	})
}

func padPayload(payload []byte) []byte {
	if len(payload) >= 8 {
		return payload[:8]
	}
	padded := make([]byte, 8)
	copy(padded, payload)
	for i := len(payload); i < 8; i++ {
		padded[i] = byte(i * 31)
	}
	return padded
}
