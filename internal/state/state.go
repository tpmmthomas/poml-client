// Package state is the deterministic ledger transition used for every branch.
package state

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/profile"
	"github.com/thomas/poml-node/internal/types"
)

func FromGenesis(genesis profile.Genesis) types.State {
	state := types.NewState()
	for id, account := range genesis.Accounts {
		state.Accounts[id] = account
	}
	for id, miner := range genesis.Miners {
		miner.Active = true
		state.Miners[id] = miner
	}
	return state
}

func ApplyTransaction(current types.State, tx types.Tx, height uint64, genesis profile.Genesis) error {
	if tx.Sender == "" {
		return errors.New("transaction has no sender")
	}
	// Responses are special service claims. Their identity is the signed
	// query/attempt context, so several responses from one miner may share the
	// same account nonce in one block and do not advance the payment nonce.
	if tx.Kind == types.TxResponse {
		if _, ok := current.Accounts[tx.Sender]; !ok {
			return errors.New("response sender account is unknown")
		}
		return applyResponse(current, tx, height, genesis)
	}
	account, ok := current.Accounts[tx.Sender]
	if !ok || account.Nonce != tx.Nonce {
		return errors.New("unknown account or nonce mismatch")
	}
	switch tx.Kind {
	case types.TxQuery:
		return applyQuery(current, account, tx, height, genesis)
	case types.TxTransfer:
		return applyTransfer(current, account, tx)
	case types.TxChallenge:
		return applyChallenge(current, account, tx, height, genesis)
	case types.TxChallengeAnswer:
		if err := applyChallengeAnswer(current, account, tx, height, genesis); err != nil {
			return err
		}
		updated := current.Accounts[tx.Sender]
		updated.Nonce++
		current.Accounts[tx.Sender] = updated
		return nil
	case types.TxMinerRegistration:
		return applyMinerRegistration(current, account, tx, height)
	default:
		return errors.New("unknown transaction kind")
	}
}

func applyQuery(current types.State, account types.Account, tx types.Tx, height uint64, genesis profile.Genesis) error {
	if tx.QueryID == "" || tx.ModelID != genesis.Raw.ModelProfileID || len(tx.Input) == 0 || tx.Expiry <= height {
		return errors.New("query is missing required fields")
	}
	if expected := types.HashBytes("input", tx.Input); expected != tx.Commitment {
		return errors.New("query input commitment mismatch")
	}
	if _, exists := current.Queries[tx.QueryID]; exists {
		return errors.New("query already exists")
	}
	fee := tx.Fee
	if fee == 0 {
		fee = genesis.Raw.QueryFee
	}
	if account.Balance < fee {
		return errors.New("insufficient query balance")
	}
	account.Balance -= fee
	account.Nonce++
	current.Accounts[tx.Sender] = account
	current.Queries[tx.QueryID] = types.Query{
		ID: tx.QueryID, Sender: tx.Sender, Input: append([]byte(nil), tx.Input...),
		Commitment: tx.Commitment, Recipient: append([]byte(nil), tx.Recipient...),
		ModelID: tx.ModelID, Fee: fee, Escrow: fee, Expiry: tx.Expiry, CreatedAt: height,
		SignedTx: codec.TxBytes(tx),
	}
	return nil
}

func applyTransfer(current types.State, account types.Account, tx types.Tx) error {
	if tx.QueryID == "" || tx.Amount == 0 || account.Balance < tx.Amount {
		return errors.New("invalid transfer")
	}
	recipient, ok := current.Accounts[tx.QueryID]
	if !ok {
		return errors.New("unknown transfer recipient")
	}
	account.Balance -= tx.Amount
	account.Nonce++
	recipient.Balance += tx.Amount
	current.Accounts[tx.Sender] = account
	current.Accounts[tx.QueryID] = recipient
	return nil
}

func applyResponse(current types.State, tx types.Tx, height uint64, genesis profile.Genesis) error {
	query, ok := current.Queries[tx.QueryID]
	if !ok || query.Settled || query.Expired || height > query.Expiry {
		return errors.New("response query is unavailable")
	}
	if claim, exists := current.Claims[tx.QueryID]; exists {
		if claim.MinerID == tx.MinerID && claim.AttemptID == tx.AttemptID && claim.Status == types.ClaimReopened {
			return errors.New("response claim was already reopened")
		}
		return errors.New("query already has a pending claim")
	}
	miner, ok := current.Miners[tx.MinerID]
	if !ok || !miner.Active {
		return errors.New("response miner is not registered")
	}
	account, ok := current.Accounts[tx.MinerID]
	if !ok || account.Balance < miner.Bond {
		return errors.New("miner cannot lock response bond")
	}
	account.Balance -= miner.Bond
	current.Accounts[tx.MinerID] = account
	miner.LockedBond += miner.Bond
	current.Miners[tx.MinerID] = miner
	current.Claims[tx.QueryID] = types.Claim{
		QueryID: tx.QueryID, MinerID: tx.MinerID, Amount: query.Escrow,
		BlockHeight: height, Deadline: height + genesis.Raw.ChallengeWindow,
		BondLocked: miner.Bond,
		Status:     types.ClaimPending, AttemptID: tx.AttemptID, Parent: tx.Parent,
		PrefixDigest: tx.PrefixDigest, Ciphertext: append([]byte(nil), tx.Ciphertext...),
		TicketHash: tx.TicketHash, Rho: append([]byte(nil), tx.Rho...),
		RhoProof: append([]byte(nil), tx.RhoProof...), CTicket: tx.CTicket,
	}
	return nil
}

func applyChallenge(current types.State, account types.Account, tx types.Tx, height uint64, genesis profile.Genesis) error {
	claim, ok := current.Claims[tx.QueryID]
	if !ok || claim.Settled || claim.Status != types.ClaimPending || height > claim.Deadline {
		return errors.New("claim cannot be challenged")
	}
	if tx.Sender == claim.MinerID {
		return errors.New("miner cannot challenge its own response")
	}
	fee := tx.Fee
	if fee == 0 {
		fee = 1
	}
	if fee > account.Balance {
		return errors.New("insufficient challenge balance")
	}
	account.Balance -= fee
	account.Nonce++
	current.Accounts[tx.Sender] = account
	claim.Challenged = true
	claim.Status = types.ClaimChallenged
	claim.Challenger = tx.Sender
	claim.ChallengeFee = fee
	window := genesis.Raw.AnswerWindow
	if window == 0 {
		window = genesis.Raw.ChallengeWindow
	}
	claim.AnswerDeadline = height + window
	current.Claims[tx.QueryID] = claim
	return nil
}

// ApplyChallengeAnswer settles a challenged response after the caller has
// verified the proof against the public statement. The state machine checks
// the statement context as well, so a valid proof cannot be attached to a
// different response claim.
func applyChallengeAnswer(current types.State, account types.Account, tx types.Tx, height uint64, genesis profile.Genesis) error {
	claim, ok := current.Claims[tx.QueryID]
	if !ok || claim.Status != types.ClaimChallenged || height > claim.AnswerDeadline {
		return errors.New("claim is not awaiting an answer")
	}
	if tx.Sender != claim.MinerID || tx.AttemptID != claim.AttemptID || len(tx.Proof) == 0 || len(tx.ProofStatement) == 0 {
		return errors.New("challenge answer does not match the claim")
	}
	if tx.Parent != claim.Parent || tx.PrefixDigest != claim.PrefixDigest || tx.TicketHash != claim.TicketHash || !bytes.Equal(tx.Ciphertext, claim.Ciphertext) {
		return errors.New("challenge answer context mismatch")
	}
	_ = account
	settleClaim(current, &claim, true)
	claim.Proof = append([]byte(nil), tx.Proof...)
	claim.ProofStatement = append([]byte(nil), tx.ProofStatement...)
	claim.Status = types.ClaimAnswered
	current.Claims[tx.QueryID] = claim
	return nil
}

func applyMinerRegistration(current types.State, account types.Account, tx types.Tx, height uint64) error {
	if tx.MinerID == "" || tx.Amount == 0 || len(tx.IdentityPublic) != 32 || len(tx.InferenceVRFPublic) == 0 || len(tx.EncryptionVRFPublic) == 0 {
		return errors.New("miner registration is incomplete")
	}
	if _, exists := current.Miners[tx.MinerID]; exists {
		return errors.New("miner is already registered")
	}
	if account.Balance < tx.Amount {
		return errors.New("insufficient registration bond")
	}
	account.Balance -= tx.Amount
	account.Nonce++
	current.Accounts[tx.Sender] = account
	current.Miners[tx.MinerID] = types.Miner{
		ID: tx.MinerID, IdentityPub: append([]byte(nil), tx.IdentityPublic...),
		InferenceVRFPub:  append([]byte(nil), tx.InferenceVRFPublic...),
		EncryptionVRFPub: append([]byte(nil), tx.EncryptionVRFPublic...),
		Bond:             tx.Amount, LockedBond: tx.Amount, RegisteredAt: height, Active: true,
	}
	return nil
}

// ApplyWinner settles the proof-bearing path. A query that already has an
// optimistic claim cannot be paid a second time by a different winner.
func ApplyWinner(current types.State, winner types.Winner, height uint64) error {
	query, ok := current.Queries[winner.QueryID]
	if !ok || query.Settled || query.Expired || query.Escrow == 0 {
		return errors.New("winner query is unavailable")
	}
	if _, exists := current.Claims[winner.QueryID]; exists {
		return errors.New("winner query already has an optimistic claim")
	}
	minerAccount, ok := current.Accounts[winner.MinerID]
	if !ok {
		return errors.New("winner miner account is unknown")
	}
	minerAccount.Balance += query.Escrow
	current.Accounts[winner.MinerID] = minerAccount
	query.Escrow = 0
	query.Settled = true
	current.Queries[winner.QueryID] = query
	_ = height
	return nil
}

// settleClaim pays escrow once and releases the response bond. A challenged
// claim also refunds its challenge fee after a valid answer.
func settleClaim(current types.State, claim *types.Claim, answered bool) {
	query, queryOK := current.Queries[claim.QueryID]
	miner, minerOK := current.Miners[claim.MinerID]
	minerAccount, minerAccountOK := current.Accounts[claim.MinerID]
	if minerOK && minerAccountOK {
		unlock := claim.BondLocked
		if unlock > miner.LockedBond {
			unlock = miner.LockedBond
		}
		miner.LockedBond -= unlock
		minerAccount.Balance += unlock
		if queryOK && query.Escrow > 0 {
			minerAccount.Balance += query.Escrow
			query.Escrow = 0
			query.Settled = true
		}
		current.Miners[claim.MinerID] = miner
		current.Accounts[claim.MinerID] = minerAccount
	}
	if answered && claim.Challenger != "" && claim.ChallengeFee > 0 {
		challenger := current.Accounts[claim.Challenger]
		challenger.Balance += claim.ChallengeFee
		current.Accounts[claim.Challenger] = challenger
	}
	if queryOK {
		current.Queries[claim.QueryID] = query
	}
	claim.BondLocked = 0
	claim.Settled = true
}

func expireClaim(current types.State, claim *types.Claim, genesis profile.Genesis) {
	query, queryOK := current.Queries[claim.QueryID]
	if queryOK && query.Escrow > 0 {
		owner := current.Accounts[query.Sender]
		owner.Balance += query.Escrow
		current.Accounts[query.Sender] = owner
		query.Escrow = 0
		query.Refunded = true
		query.Expired = true
		current.Queries[claim.QueryID] = query
	}
	miner := current.Miners[claim.MinerID]
	minerAccount := current.Accounts[claim.MinerID]
	slash := claim.BondLocked
	if genesis.Raw.BondSlash > 0 && genesis.Raw.BondSlash < slash {
		slash = genesis.Raw.BondSlash
	}
	if slash > miner.LockedBond {
		slash = miner.LockedBond
	}
	miner.LockedBond -= slash
	minerAccount.Balance += claim.BondLocked - slash
	current.Miners[claim.MinerID] = miner
	current.Accounts[claim.MinerID] = minerAccount
	if claim.Challenger != "" {
		reward := current.Accounts[claim.Challenger]
		reward.Balance += slash + claim.ChallengeFee
		current.Accounts[claim.Challenger] = reward
	}
	claim.BondLocked = 0
	claim.Settled = true
	claim.Refunded = true
	claim.Slashed = true
}

// AdvanceDeadlines applies only height-based transitions. It is called after
// every accepted block, so a stopped chain cannot expire or slash anything.
// The optional genesis preserves the old two-argument API for callers that do
// not need configured slash accounting.
func AdvanceDeadlines(current types.State, height uint64, configs ...profile.Genesis) {
	var genesis profile.Genesis
	if len(configs) > 0 {
		genesis = configs[0]
	}
	for id, claim := range current.Claims {
		if claim.Settled {
			continue
		}
		if claim.Status == types.ClaimPending && height > claim.Deadline {
			settleClaim(current, &claim, false)
			claim.Status = types.ClaimSettled
			current.Claims[id] = claim
			continue
		}
		if claim.Status == types.ClaimChallenged && height > claim.AnswerDeadline {
			expireClaim(current, &claim, genesis)
			claim.Status = types.ClaimSlashed
			current.Claims[id] = claim
		}
	}
}

func Root(current types.State) types.Hash {
	var buffer bytes.Buffer
	writeU64(&buffer, current.Height)
	accountIDs := sortedKeys(current.Accounts)
	for _, id := range accountIDs {
		account := current.Accounts[id]
		writeString(&buffer, id)
		writeU64(&buffer, account.Balance)
		writeU64(&buffer, account.Nonce)
		writeBytes(&buffer, account.Public)
	}
	minerIDs := sortedKeys(current.Miners)
	for _, id := range minerIDs {
		miner := current.Miners[id]
		writeString(&buffer, id)
		writeBytes(&buffer, miner.IdentityPub)
		writeBytes(&buffer, miner.InferenceVRFPub)
		writeBytes(&buffer, miner.EncryptionVRFPub)
		writeU64(&buffer, miner.Bond)
		writeU64(&buffer, miner.LockedBond)
		writeU64(&buffer, miner.RegisteredAt)
		buffer.WriteByte(boolByte(miner.Active))
	}
	queryIDs := sortedKeys(current.Queries)
	for _, id := range queryIDs {
		query := current.Queries[id]
		writeString(&buffer, id)
		writeString(&buffer, query.Sender)
		writeBytes(&buffer, query.Input)
		buffer.Write(query.Commitment[:])
		writeBytes(&buffer, query.Recipient)
		writeString(&buffer, query.ModelID)
		writeU64(&buffer, query.Fee)
		writeU64(&buffer, query.Expiry)
		writeU64(&buffer, query.CreatedAt)
		buffer.WriteByte(boolByte(query.Settled))
		buffer.WriteByte(boolByte(query.Refunded))
		buffer.WriteByte(boolByte(query.Expired))
		writeU64(&buffer, query.Escrow)
		writeBytes(&buffer, query.SignedTx)
	}
	claimIDs := sortedKeys(current.Claims)
	for _, id := range claimIDs {
		claim := current.Claims[id]
		writeString(&buffer, id)
		writeString(&buffer, claim.MinerID)
		writeU64(&buffer, claim.Amount)
		writeU64(&buffer, claim.BlockHeight)
		writeU64(&buffer, claim.Deadline)
		buffer.WriteByte(boolByte(claim.Challenged))
		buffer.WriteByte(boolByte(claim.Settled))
		buffer.WriteByte(byte(claim.Status))
		writeString(&buffer, claim.AttemptID)
		buffer.Write(claim.Parent[:])
		buffer.Write(claim.PrefixDigest[:])
		writeBytes(&buffer, claim.Ciphertext)
		buffer.Write(claim.TicketHash[:])
		writeBytes(&buffer, claim.Rho)
		writeBytes(&buffer, claim.RhoProof)
		writeU64(&buffer, claim.CTicket)
		writeString(&buffer, claim.Challenger)
		writeU64(&buffer, claim.ChallengeFee)
		writeU64(&buffer, claim.AnswerDeadline)
		writeU64(&buffer, claim.BondLocked)
		writeBytes(&buffer, claim.ProofStatement)
		writeBytes(&buffer, claim.Proof)
		buffer.WriteByte(boolByte(claim.Refunded))
		buffer.WriteByte(boolByte(claim.Slashed))
	}
	return types.HashBytes("state", buffer.Bytes())
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeU64(buffer *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	buffer.Write(encoded[:])
}

func writeBytes(buffer *bytes.Buffer, value []byte) {
	writeU64(buffer, uint64(len(value)))
	buffer.Write(value)
}

func writeString(buffer *bytes.Buffer, value string) { writeBytes(buffer, []byte(value)) }

func boolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}

func ValidateQueryCommitment(query types.Query) error {
	if query.ID == "" || len(query.Input) == 0 {
		return errors.New("query is incomplete")
	}
	if expected := types.HashBytes("input", query.Input); expected != query.Commitment {
		return fmt.Errorf("query %s has an invalid input commitment", query.ID)
	}
	return nil
}
