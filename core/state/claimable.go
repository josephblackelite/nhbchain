package state

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/claimable"
	"nhbchain/core/types"
	"nhbchain/native/escrow"
)

func claimableStorageKey(id [32]byte) []byte {
	buf := make([]byte, len(claimableRecordPrefix)+len(id))
	copy(buf, claimableRecordPrefix)
	copy(buf[len(claimableRecordPrefix):], id[:])
	return ethcrypto.Keccak256(buf)
}

func claimableNonceKey(payer [20]byte) []byte {
	buf := make([]byte, len(claimableNoncePrefix)+len(payer))
	copy(buf, claimableNoncePrefix)
	copy(buf[len(claimableNoncePrefix):], payer[:])
	return ethcrypto.Keccak256(buf)
}

type storedClaimable struct {
	ID            [32]byte
	Payer         [20]byte
	Token         string
	Amount        *big.Int
	HashLock      [32]byte
	RecipientHint [32]byte
	Deadline      *big.Int
	CreatedAt     *big.Int
	Status        uint8
}

func newStoredClaimable(c *claimable.Claimable) *storedClaimable {
	if c == nil {
		return nil
	}
	amount := big.NewInt(0)
	if c.Amount != nil {
		amount = new(big.Int).Set(c.Amount)
	}
	return &storedClaimable{
		ID:            c.ID,
		Payer:         c.Payer,
		Token:         c.Token,
		Amount:        amount,
		HashLock:      c.HashLock,
		RecipientHint: c.RecipientHint,
		Deadline:      big.NewInt(c.Deadline),
		CreatedAt:     big.NewInt(c.CreatedAt),
		Status:        uint8(c.Status),
	}
}

func (s *storedClaimable) toClaimable() (*claimable.Claimable, error) {
	if s == nil {
		return nil, fmt.Errorf("claimable: nil storage record")
	}
	normalized, err := escrow.NormalizeToken(s.Token)
	if err != nil {
		return nil, claimable.ErrInvalidToken
	}
	out := &claimable.Claimable{
		ID:            s.ID,
		Payer:         s.Payer,
		Token:         normalized,
		Amount:        big.NewInt(0),
		HashLock:      s.HashLock,
		RecipientHint: s.RecipientHint,
		Status:        claimable.ClaimStatus(s.Status),
	}
	if s.Amount != nil {
		out.Amount = new(big.Int).Set(s.Amount)
	}
	if s.Deadline != nil {
		out.Deadline = s.Deadline.Int64()
	}
	if s.CreatedAt != nil {
		out.CreatedAt = s.CreatedAt.Int64()
	}
	if !out.Status.Valid() {
		return nil, claimable.ErrInvalidState
	}
	return out, nil
}

func cloneAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	cloned := *acc
	if acc.BalanceNHB != nil {
		cloned.BalanceNHB = new(big.Int).Set(acc.BalanceNHB)
	} else {
		cloned.BalanceNHB = big.NewInt(0)
	}
	if acc.BalanceZNHB != nil {
		cloned.BalanceZNHB = new(big.Int).Set(acc.BalanceZNHB)
	} else {
		cloned.BalanceZNHB = big.NewInt(0)
	}
	if acc.Stake != nil {
		cloned.Stake = new(big.Int).Set(acc.Stake)
	} else {
		cloned.Stake = big.NewInt(0)
	}
	return &cloned
}

func (m *Manager) nextClaimableID(payer [20]byte) ([32]byte, uint64, error) {
	key := claimableNonceKey(payer)
	current, err := m.loadBigInt(key)
	if err != nil {
		return [32]byte{}, 0, err
	}
	if current.Sign() < 0 {
		return [32]byte{}, 0, fmt.Errorf("claimable: negative nonce state")
	}
	if current.BitLen() > 63 {
		return [32]byte{}, 0, fmt.Errorf("claimable: nonce overflow")
	}
	nonce := current.Uint64()
	buf := make([]byte, len(payer)+8)
	copy(buf, payer[:])
	binary.BigEndian.PutUint64(buf[len(payer):], nonce)
	hash := ethcrypto.Keccak256(buf)
	var id [32]byte
	copy(id[:], hash)
	next := new(big.Int).SetUint64(nonce + 1)
	if err := m.writeBigInt(key, next); err != nil {
		return [32]byte{}, 0, err
	}
	return id, nonce, nil
}

func (m *Manager) revertClaimableNonce(payer [20]byte, nonce uint64) {
	key := claimableNonceKey(payer)
	_ = m.writeBigInt(key, new(big.Int).SetUint64(nonce))
}

func (m *Manager) ClaimablePut(c *claimable.Claimable) error {
	if c == nil {
		return fmt.Errorf("claimable: nil value")
	}
	if !c.Status.Valid() {
		return fmt.Errorf("claimable: invalid status")
	}
	normalized, err := escrow.NormalizeToken(c.Token)
	if err != nil {
		return claimable.ErrInvalidToken
	}
	record := newStoredClaimable(&claimable.Claimable{
		ID:            c.ID,
		Payer:         c.Payer,
		Token:         normalized,
		Amount:        c.Amount,
		HashLock:      c.HashLock,
		RecipientHint: c.RecipientHint,
		Deadline:      c.Deadline,
		CreatedAt:     c.CreatedAt,
		Status:        c.Status,
	})
	encoded, err := rlp.EncodeToBytes(record)
	if err != nil {
		return err
	}
	return m.trie.Update(claimableStorageKey(c.ID), encoded)
}

func (m *Manager) ClaimableGet(id [32]byte) (*claimable.Claimable, bool) {
	data, err := m.trie.Get(claimableStorageKey(id))
	if err != nil || len(data) == 0 {
		return nil, false
	}
	stored := new(storedClaimable)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false
	}
	record, err := stored.toClaimable()
	if err != nil {
		return nil, false
	}
	return record, true
}

func (m *Manager) ClaimableCredit(payer [20]byte, token string, amt *big.Int) error {
	if amt == nil || amt.Sign() <= 0 {
		return claimable.ErrInvalidAmount
	}
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return claimable.ErrInvalidToken
	}
	vault, err := escrowModuleAddress(normalized)
	if err != nil {
		return err
	}
	payerAcc, err := m.GetAccount(payer[:])
	if err != nil {
		return err
	}
	vaultAcc, err := m.GetAccount(vault[:])
	if err != nil {
		return err
	}
	originalPayer := cloneAccount(payerAcc)
	originalVault := cloneAccount(vaultAcc)
	payerAcc = cloneAccount(payerAcc)
	vaultAcc = cloneAccount(vaultAcc)

	rollbacks := make([]func(), 0, 2)
	revert := func() {
		for i := len(rollbacks) - 1; i >= 0; i-- {
			if rollbacks[i] != nil {
				rollbacks[i]()
			}
		}
	}
	switch normalized {
	case "NHB":
		rollback, subErr := MustSubBalance(payerAcc.BalanceNHB, amt)
		if subErr != nil {
			if errors.Is(subErr, ErrInsufficientBalance) {
				return claimable.ErrInsufficientFunds
			}
			return subErr
		}
		rollbacks = append(rollbacks, rollback)
		rollback, addErr := MustAddBalance(vaultAcc.BalanceNHB, amt)
		if addErr != nil {
			revert()
			return addErr
		}
		rollbacks = append(rollbacks, rollback)
	case "ZNHB":
		rollback, subErr := MustSubBalance(payerAcc.BalanceZNHB, amt)
		if subErr != nil {
			if errors.Is(subErr, ErrInsufficientBalance) {
				return claimable.ErrInsufficientFunds
			}
			return subErr
		}
		rollbacks = append(rollbacks, rollback)
		rollback, addErr := MustAddBalance(vaultAcc.BalanceZNHB, amt)
		if addErr != nil {
			revert()
			return addErr
		}
		rollbacks = append(rollbacks, rollback)
	default:
		return claimable.ErrInvalidToken
	}
	if err := m.PutAccount(payer[:], payerAcc); err != nil {
		revert()
		return err
	}
	if err := m.PutAccount(vault[:], vaultAcc); err != nil {
		revert()
		if restoreErr := m.PutAccount(payer[:], originalPayer); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("claimable: rollback payer: %w", restoreErr))
		}
		if restoreErr := m.PutAccount(vault[:], originalVault); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("claimable: rollback vault: %w", restoreErr))
		}
		return err
	}
	return nil
}

func (m *Manager) ClaimableDebit(token string, amt *big.Int, recipient [20]byte) error {
	if amt == nil || amt.Sign() <= 0 {
		return claimable.ErrInvalidAmount
	}
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return claimable.ErrInvalidToken
	}
	vault, err := escrowModuleAddress(normalized)
	if err != nil {
		return err
	}
	vaultAcc, err := m.GetAccount(vault[:])
	if err != nil {
		return err
	}
	recipientAcc, err := m.GetAccount(recipient[:])
	if err != nil {
		return err
	}
	originalVault := cloneAccount(vaultAcc)
	originalRecipient := cloneAccount(recipientAcc)
	vaultAcc = cloneAccount(vaultAcc)
	recipientAcc = cloneAccount(recipientAcc)

	rollbacks := make([]func(), 0, 2)
	revert := func() {
		for i := len(rollbacks) - 1; i >= 0; i-- {
			if rollbacks[i] != nil {
				rollbacks[i]()
			}
		}
	}
	switch normalized {
	case "NHB":
		rollback, subErr := MustSubBalance(vaultAcc.BalanceNHB, amt)
		if subErr != nil {
			if errors.Is(subErr, ErrInsufficientBalance) {
				return claimable.ErrInsufficientFunds
			}
			return subErr
		}
		rollbacks = append(rollbacks, rollback)
		rollback, addErr := MustAddBalance(recipientAcc.BalanceNHB, amt)
		if addErr != nil {
			revert()
			return addErr
		}
		rollbacks = append(rollbacks, rollback)
	case "ZNHB":
		rollback, subErr := MustSubBalance(vaultAcc.BalanceZNHB, amt)
		if subErr != nil {
			if errors.Is(subErr, ErrInsufficientBalance) {
				return claimable.ErrInsufficientFunds
			}
			return subErr
		}
		rollbacks = append(rollbacks, rollback)
		rollback, addErr := MustAddBalance(recipientAcc.BalanceZNHB, amt)
		if addErr != nil {
			revert()
			return addErr
		}
		rollbacks = append(rollbacks, rollback)
	default:
		return claimable.ErrInvalidToken
	}
	if err := m.PutAccount(vault[:], vaultAcc); err != nil {
		revert()
		return err
	}
	if err := m.PutAccount(recipient[:], recipientAcc); err != nil {
		revert()
		if restoreErr := m.PutAccount(vault[:], originalVault); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("claimable: rollback vault: %w", restoreErr))
		}
		if restoreErr := m.PutAccount(recipient[:], originalRecipient); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("claimable: rollback recipient: %w", restoreErr))
		}
		return err
	}
	return nil
}

func (m *Manager) CreateClaimable(payer [20]byte, token string, amount *big.Int, hashLock [32]byte, deadline int64, hint [32]byte) (*claimable.Claimable, error) {
	if amount == nil || amount.Sign() <= 0 {
		return nil, claimable.ErrInvalidAmount
	}
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return nil, claimable.ErrInvalidToken
	}
	id, nonce, err := m.nextClaimableID(payer)
	if err != nil {
		return nil, err
	}
	rollback := func() {
		m.revertClaimableNonce(payer, nonce)
	}
	if err := m.ClaimableCredit(payer, normalized, amount); err != nil {
		rollback()
		return nil, err
	}
	record := &claimable.Claimable{
		ID:            id,
		Payer:         payer,
		Token:         normalized,
		Amount:        new(big.Int).Set(amount),
		HashLock:      hashLock,
		RecipientHint: hint,
		Deadline:      deadline,
		CreatedAt:     time.Now().Unix(),
		Status:        claimable.ClaimStatusInit,
	}
	if err := m.ClaimablePut(record); err != nil {
		_ = m.ClaimableDebit(normalized, amount, payer)
		rollback()
		return nil, err
	}
	return record, nil
}

func (m *Manager) ClaimableClaim(id [32]byte, preimage []byte, payee [20]byte) (*claimable.Claimable, bool, error) {
	record, ok := m.ClaimableGet(id)
	if !ok {
		return nil, false, claimable.ErrNotFound
	}
	if record.Status == claimable.ClaimStatusClaimed {
		return record, false, nil
	}
	if record.Status != claimable.ClaimStatusInit {
		return record, false, claimable.ErrInvalidState
	}
	hash := ethcrypto.Keccak256(preimage)
	if !bytes.Equal(hash, record.HashLock[:]) {
		return nil, false, claimable.ErrInvalidPreimage
	}
	if err := m.ClaimableDebit(record.Token, record.Amount, payee); err != nil {
		return nil, false, err
	}
	record.Status = claimable.ClaimStatusClaimed
	if err := m.ClaimablePut(record); err != nil {
		_ = m.ClaimableCredit(payee, record.Token, record.Amount)
		return nil, false, err
	}
	return record, true, nil
}

func (m *Manager) ClaimableCancel(id [32]byte, caller [20]byte, now int64) (*claimable.Claimable, bool, error) {
	record, ok := m.ClaimableGet(id)
	if !ok {
		return nil, false, claimable.ErrNotFound
	}
	if record.Status == claimable.ClaimStatusCancelled {
		return record, false, nil
	}
	if record.Status == claimable.ClaimStatusExpired {
		return record, false, nil
	}
	if record.Status != claimable.ClaimStatusInit {
		return record, false, claimable.ErrInvalidState
	}
	if caller != record.Payer {
		return nil, false, claimable.ErrUnauthorized
	}
	if now > record.Deadline {
		return nil, false, claimable.ErrDeadlineExceeded
	}
	if err := m.ClaimableDebit(record.Token, record.Amount, record.Payer); err != nil {
		return nil, false, err
	}
	record.Status = claimable.ClaimStatusCancelled
	if err := m.ClaimablePut(record); err != nil {
		_ = m.ClaimableCredit(record.Payer, record.Token, record.Amount)
		return nil, false, err
	}
	return record, true, nil
}

func (m *Manager) ClaimableExpire(id [32]byte, now int64) (*claimable.Claimable, bool, error) {
	record, ok := m.ClaimableGet(id)
	if !ok {
		return nil, false, claimable.ErrNotFound
	}
	if record.Status == claimable.ClaimStatusExpired {
		return record, false, nil
	}
	if record.Status == claimable.ClaimStatusCancelled {
		return record, false, nil
	}
	if record.Status != claimable.ClaimStatusInit {
		return record, false, claimable.ErrInvalidState
	}
	if now < record.Deadline {
		return nil, false, claimable.ErrNotExpired
	}
	if err := m.ClaimableDebit(record.Token, record.Amount, record.Payer); err != nil {
		return nil, false, err
	}
	record.Status = claimable.ClaimStatusExpired
	if err := m.ClaimablePut(record); err != nil {
		_ = m.ClaimableCredit(record.Payer, record.Token, record.Amount)
		return nil, false, err
	}
	return record, true, nil
}
