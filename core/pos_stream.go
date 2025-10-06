package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"nhbchain/core/types"
)

const posFinalityHistoryLimit = 2048

// POSFinalityStatus indicates the state of a POS intent lifecycle.
type POSFinalityStatus string

const (
	// POSFinalityStatusPending marks intents that are accepted into the mempool.
	POSFinalityStatusPending POSFinalityStatus = "pending"
	// POSFinalityStatusFinalized marks intents that have been committed in a finalized block.
	POSFinalityStatusFinalized POSFinalityStatus = "finalized"
)

// POSFinalityUpdate captures lifecycle transitions for POS payment intents.
type POSFinalityUpdate struct {
	Sequence  uint64
	Cursor    string
	IntentRef []byte
	TxHash    []byte
	Status    POSFinalityStatus
	BlockHash []byte
	Height    uint64
	Timestamp int64
}

func clonePOSFinalityUpdate(update POSFinalityUpdate) POSFinalityUpdate {
	cloned := update
	if len(update.IntentRef) > 0 {
		cloned.IntentRef = append([]byte(nil), update.IntentRef...)
	}
	if len(update.TxHash) > 0 {
		cloned.TxHash = append([]byte(nil), update.TxHash...)
	}
	if len(update.BlockHash) > 0 {
		cloned.BlockHash = append([]byte(nil), update.BlockHash...)
	}
	return cloned
}

func (n *Node) publishPOSFinality(update POSFinalityUpdate) {
	if n == nil || len(update.IntentRef) == 0 {
		return
	}

	n.posStreamMu.Lock()
	if n.posStreamSubs == nil {
		n.posStreamSubs = make(map[uint64]chan POSFinalityUpdate)
	}
	n.posStreamSeq++
	update.Sequence = n.posStreamSeq
	update.Cursor = strconv.FormatUint(update.Sequence, 10)
	stored := clonePOSFinalityUpdate(update)
	n.posStreamHistory = append(n.posStreamHistory, stored)
	if len(n.posStreamHistory) > posFinalityHistoryLimit {
		excess := len(n.posStreamHistory) - posFinalityHistoryLimit
		trimmed := make([]POSFinalityUpdate, posFinalityHistoryLimit)
		copy(trimmed, n.posStreamHistory[excess:])
		n.posStreamHistory = trimmed
	}
	subscribers := make([]chan POSFinalityUpdate, 0, len(n.posStreamSubs))
	for _, ch := range n.posStreamSubs {
		subscribers = append(subscribers, ch)
	}
	n.posStreamMu.Unlock()

	broadcast := clonePOSFinalityUpdate(update)
	for _, ch := range subscribers {
		select {
		case ch <- broadcast:
		default:
		}
	}
}

func (n *Node) publishPOSFinalityFinalized(block *types.Block) {
	if n == nil || block == nil || block.Header == nil {
		return
	}
	blockHash, err := block.Header.Hash()
	if err != nil {
		return
	}
	timestamp := block.Header.Timestamp
	height := block.Header.Height
	for _, tx := range block.Transactions {
		if tx == nil || len(tx.IntentRef) == 0 {
			continue
		}
		hash, err := tx.Hash()
		if err != nil {
			continue
		}
		n.publishPOSFinality(POSFinalityUpdate{
			IntentRef: append([]byte(nil), tx.IntentRef...),
			TxHash:    append([]byte(nil), hash...),
			Status:    POSFinalityStatusFinalized,
			BlockHash: append([]byte(nil), blockHash...),
			Height:    height,
			Timestamp: timestamp,
		})
	}
}

// POSFinalitySubscribe registers a subscriber for POS intent lifecycle updates starting after the supplied cursor.
func (n *Node) POSFinalitySubscribe(ctx context.Context, cursor string) (<-chan POSFinalityUpdate, func(), []POSFinalityUpdate, error) {
	if n == nil {
		return nil, nil, nil, fmt.Errorf("node not initialised")
	}
	updates := make(chan POSFinalityUpdate, 32)

	var since uint64
	if trimmed := strings.TrimSpace(cursor); trimmed != "" {
		if parsed, err := strconv.ParseUint(trimmed, 10, 64); err == nil {
			since = parsed
		}
	}

	n.posStreamMu.Lock()
	if n.posStreamSubs == nil {
		n.posStreamSubs = make(map[uint64]chan POSFinalityUpdate)
	}
	id := n.posStreamNextID
	n.posStreamNextID++
	n.posStreamSubs[id] = updates
	history := make([]POSFinalityUpdate, len(n.posStreamHistory))
	copy(history, n.posStreamHistory)
	n.posStreamMu.Unlock()

	backlog := make([]POSFinalityUpdate, 0, len(history))
	for _, entry := range history {
		if entry.Sequence > since {
			backlog = append(backlog, clonePOSFinalityUpdate(entry))
		}
	}

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			n.posStreamMu.Lock()
			sub, ok := n.posStreamSubs[id]
			if ok {
				delete(n.posStreamSubs, id)
				close(sub)
			}
			n.posStreamMu.Unlock()
		})
	}

	if ctx != nil {
		go func() {
			<-ctx.Done()
			cancel()
		}()
	}

	return updates, cancel, backlog, nil
}
