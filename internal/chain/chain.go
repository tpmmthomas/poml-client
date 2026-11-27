// Package chain stores valid branches and chooses the local canonical tip.
package chain

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/consensus"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/events"
	"github.com/thomas/poml-node/internal/profile"
	proofutil "github.com/thomas/poml-node/internal/proof"
	"github.com/thomas/poml-node/internal/state"
	"github.com/thomas/poml-node/internal/store"
	"github.com/thomas/poml-node/internal/types"
)

type Chain struct {
	mu        sync.RWMutex
	genesis   profile.Genesis
	store     *store.Store
	events    *events.Writer
	tip       types.Hash
	tipHeight uint64
	arrival   map[types.Hash]time.Time
}

type Result struct {
	BlockID     types.Hash
	Adopted     bool
	Height      uint64
	DetachedTxs []types.Tx
	AttachedTxs []types.Tx
}

func Open(genesis profile.Genesis, durable *store.Store, eventWriter *events.Writer) (*Chain, error) {
	chain := &Chain{
		genesis: genesis, store: durable, events: eventWriter,
		tip: genesis.Hash, arrival: make(map[types.Hash]time.Time),
	}
	storedGenesis, genesisErr := durable.Genesis()
	if genesisErr == nil && storedGenesis != genesis.Hash {
		return nil, errors.New("stored data belongs to a different genesis")
	}
	if genesisErr != nil && !errors.Is(genesisErr, os.ErrNotExist) {
		return nil, fmt.Errorf("load stored genesis: %w", genesisErr)
	}
	tip, tipErr := durable.Tip()
	if tipErr == nil {
		if genesisErr != nil {
			return nil, errors.New("stored tip exists without stored genesis")
		}
		if tip == genesis.Hash {
			if _, stateErr := durable.State(genesis.Hash); stateErr != nil {
				return nil, fmt.Errorf("load stored genesis state: %w", stateErr)
			}
			chain.tip = tip
			chain.tipHeight = 0
			return chain, nil
		}
		block, blockErr := durable.Block(tip)
		if blockErr != nil {
			return nil, fmt.Errorf("load stored tip: %w", blockErr)
		}
		if _, stateErr := durable.State(tip); stateErr != nil {
			return nil, fmt.Errorf("load stored tip state: %w", stateErr)
		}
		chain.tip = tip
		chain.tipHeight = block.Header.Height
		return chain, nil
	}
	if !errors.Is(tipErr, os.ErrNotExist) {
		return nil, fmt.Errorf("load stored tip: %w", tipErr)
	}
	if genesisErr == nil {
		if _, stateErr := durable.State(genesis.Hash); stateErr != nil {
			return nil, fmt.Errorf("load stored genesis state: %w", stateErr)
		}
		if err := durable.SetTip(genesis.Hash); err != nil {
			return nil, err
		}
		return chain, nil
	}
	if err := durable.PutGenesis(genesis.Hash, state.FromGenesis(genesis)); err != nil {
		return nil, err
	}
	return chain, nil
}

func (c *Chain) Tip() (types.Hash, uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tip, c.tipHeight
}

func (c *Chain) StateAt(id types.Hash) (types.State, error) {
	return c.store.State(id)
}

func (c *Chain) Block(id types.Hash) (types.Block, error) { return c.store.Block(id) }

// HasBlock reports whether a complete block body is available locally.
func (c *Chain) HasBlock(id types.Hash) bool {
	return id == c.genesis.Hash || c.store.HasBlock(id)
}

func (c *Chain) Close() error { return nil }

func (c *Chain) TipState() (types.State, error) {
	c.mu.RLock()
	tip := c.tip
	c.mu.RUnlock()
	return c.store.State(tip)
}

func (c *Chain) AddBlock(block types.Block) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	blockID := codec.BlockID(block)
	if c.store.HasBlock(blockID) {
		return Result{BlockID: blockID, Height: block.Header.Height}, nil
	}
	parentState, parentHeight, err := c.parentState(block.Header.Parent)
	if err != nil {
		return Result{}, err
	}
	if err := c.validateBlock(block, blockID, parentState, parentHeight); err != nil {
		c.emit("block_reject", "rejected", err.Error(), block, blockID)
		return Result{}, err
	}
	nextState := parentState.Copy()
	for _, tx := range block.Prefix {
		if err := state.ApplyTransaction(nextState, tx, block.Header.Height, c.genesis); err != nil {
			return Result{}, fmt.Errorf("prefix transaction: %w", err)
		}
	}
	for _, tx := range block.Responses {
		if err := state.ApplyTransaction(nextState, tx, block.Header.Height, c.genesis); err != nil {
			return Result{}, fmt.Errorf("response transaction: %w", err)
		}
	}
	if err := state.ApplyWinner(nextState, block.Winner, block.Header.Height); err != nil {
		return Result{}, err
	}
	nextState.Height = block.Header.Height
	state.AdvanceDeadlines(nextState, block.Header.Height)
	if err := c.store.PutBlock(block, nextState); err != nil {
		return Result{}, err
	}
	arrival := time.Now()
	c.arrival[blockID] = arrival
	adopted := c.shouldAdopt(blockID, block.Header.Height, arrival)
	oldTip := c.tip
	var detachedTxs, attachedTxs []types.Tx
	if adopted {
		var transitionErr error
		detachedTxs, attachedTxs, transitionErr = c.transitionTransactions(oldTip, blockID)
		if transitionErr != nil {
			return Result{}, transitionErr
		}
		if err := c.store.SetTip(blockID); err != nil {
			return Result{}, err
		}
		c.tip, c.tipHeight = blockID, block.Header.Height
		c.emit("block_adopt", "accepted", "canonical tip changed", block, blockID)
		if oldTip != c.genesis.Hash && oldTip != block.Header.Parent {
			c.emit("reorg", "accepted", "longer or earlier branch selected", block, blockID)
		}
	} else {
		c.emit("fork", "accepted", "valid side branch stored", block, blockID)
	}
	return Result{BlockID: blockID, Adopted: adopted, Height: block.Header.Height,
		DetachedTxs: detachedTxs, AttachedTxs: attachedTxs}, nil
}

// transitionTransactions returns ordinary transactions that leave and enter
// the canonical branch when a new tip is selected. The chain keeps every
// branch, so the node can rebuild its mempool after a reorganization.
func (c *Chain) transitionTransactions(oldTip, newTip types.Hash) ([]types.Tx, []types.Tx, error) {
	oldHeight := c.tipHeight
	newBlock, err := c.store.Block(newTip)
	if err != nil {
		return nil, nil, err
	}
	newHeight := newBlock.Header.Height
	oldID, newID := oldTip, newTip
	var detachedBlocks, attachedBlocks []types.Block
	for oldHeight > newHeight {
		block, blockErr := c.store.Block(oldID)
		if blockErr != nil {
			return nil, nil, blockErr
		}
		detachedBlocks = append(detachedBlocks, block)
		oldID, oldHeight = block.Header.Parent, oldHeight-1
	}
	for newHeight > oldHeight {
		block, blockErr := c.store.Block(newID)
		if blockErr != nil {
			return nil, nil, blockErr
		}
		attachedBlocks = append(attachedBlocks, block)
		newID, newHeight = block.Header.Parent, newHeight-1
	}
	for oldID != newID {
		oldBlock, oldErr := c.store.Block(oldID)
		if oldErr != nil {
			return nil, nil, oldErr
		}
		newBlock, newErr := c.store.Block(newID)
		if newErr != nil {
			return nil, nil, newErr
		}
		detachedBlocks = append(detachedBlocks, oldBlock)
		attachedBlocks = append(attachedBlocks, newBlock)
		oldID, newID = oldBlock.Header.Parent, newBlock.Header.Parent
	}
	return transactionsFromBlocks(detachedBlocks), transactionsFromBlocks(reverseBlocks(attachedBlocks)), nil
}

func transactionsFromBlocks(blocks []types.Block) []types.Tx {
	transactions := make([]types.Tx, 0)
	seen := make(map[types.Hash]struct{})
	for _, block := range blocks {
		for _, tx := range append(append([]types.Tx{}, block.Prefix...), block.Responses...) {
			id := codec.TxID(tx)
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			transactions = append(transactions, tx)
		}
	}
	return transactions
}

func reverseBlocks(blocks []types.Block) []types.Block {
	for left, right := 0, len(blocks)-1; left < right; left, right = left+1, right-1 {
		blocks[left], blocks[right] = blocks[right], blocks[left]
	}
	return blocks
}

func (c *Chain) parentState(parent types.Hash) (types.State, uint64, error) {
	if parent == c.genesis.Hash {
		return state.FromGenesis(c.genesis), 0, nil
	}
	parentBlock, err := c.store.Block(parent)
	if err != nil {
		return types.State{}, 0, errors.New("unknown parent")
	}
	parentState, err := c.store.State(parent)
	if err != nil {
		return types.State{}, 0, err
	}
	return parentState, parentBlock.Header.Height, nil
}

func (c *Chain) validateBlock(block types.Block, blockID types.Hash, parent types.State, parentHeight uint64) error {
	if block.Header.Height != parentHeight+1 || block.Header.Parent == blockID {
		return errors.New("invalid block height or self-parent")
	}
	if block.Header.PrefixDigest != codec.PrefixDigest(block.Header.Parent, block.Prefix) {
		return errors.New("prefix digest mismatch")
	}
	if block.Header.ClaimDigest != codec.ClaimDigest(block.Responses) {
		return errors.New("claim digest mismatch")
	}
	if block.Header.BodyDigest != codec.BodyDigest(block) {
		return errors.New("body digest mismatch")
	}
	if len(codec.BlockBytes(block)) > int(c.genesis.Raw.MaxBlockBytes) {
		return errors.New("block exceeds configured byte limit")
	}
	if len(block.Responses) > int(c.genesis.Raw.ResponseLimit) {
		return errors.New("response limit exceeded")
	}
	miner, ok := c.genesis.Miners[block.Header.Producer]
	if !ok || block.Winner.MinerID != block.Header.Producer {
		return errors.New("unknown block producer")
	}
	if err := cryptoutil.Verify(miner.IdentityPub, block.Signature, "block", codec.BlockUnsignedBytes(block)); err != nil {
		return err
	}
	for _, tx := range block.Prefix {
		if err := verifyTransaction(tx, parent, c.genesis); err != nil {
			return err
		}
	}
	for _, tx := range block.Responses {
		if tx.Kind != types.TxResponse || tx.MinerID != block.Header.Producer {
			return errors.New("late suffix contains a non-own response")
		}
		if err := cryptoutil.Verify(miner.IdentityPub, tx.Signature, "transaction", codec.TxSigningBytes(tx)); err != nil {
			return err
		}
	}
	winner := block.Winner
	if winner.Parent != block.Header.Parent || winner.PrefixDigest != block.Header.PrefixDigest || winner.ModelID != c.genesis.Raw.ModelProfileID || winner.ChainID != c.genesis.Raw.ChainID || winner.ProfileDigest != c.genesis.Raw.Model.Digest() {
		return errors.New("winner context mismatch")
	}
	query, queryFound := queryContext(parent, block.Prefix, winner.QueryID)
	if !queryFound {
		return errors.New("winner query is not available in the block state")
	}
	if winner.InputCommitment != query.Commitment || !bytes.Equal(winner.Recipient, query.Recipient) {
		return errors.New("winner query commitment or recipient mismatch")
	}
	if !bytes.Equal(winner.InferenceVRFPublic, miner.InferenceVRFPub) || !bytes.Equal(winner.EncryptionVRFPublic, miner.EncryptionVRFPub) {
		return errors.New("winner miner randomness key mismatch")
	}
	attemptInput := codec.AttemptInput(winner.Parent, winner.PrefixDigest, winner.ModelID, winner.QueryID, winner.MinerID, winner.AttemptID)
	if err := cryptoutil.VRFVerify(miner.InferenceVRFPub, attemptInput, winner.Rho, winner.RhoProof); err != nil {
		return fmt.Errorf("winner VRF: %w", err)
	}
	if c.genesis.Raw.Model.UsesProofBundle() {
		if winner.ProofProfile != c.genesis.Raw.Model.CertificateProfile || winner.ProofSetupDigest != c.genesis.Raw.Model.Digest() {
			return errors.New("winner proof profile or setup digest mismatch")
		}
		expectedEncryptionInput := types.HashBytes("encryption-input", attemptInput)
		if !bytes.Equal(winner.EncryptionVRFInput, expectedEncryptionInput[:]) {
			return errors.New("encryption VRF input mismatch")
		}
		if winner.OutputCommitment == (types.Hash{}) {
			return errors.New("winner output commitment is empty")
		}
		if len(winner.Ciphertext) < 32 {
			return errors.New("winner ciphertext is empty")
		}
	}
	if expected := types.HashBytes("ticket", codec.TicketBytes(winner)); expected != winner.TicketHash || winner.TicketHash != block.Header.TicketHash {
		return errors.New("winner ticket hash mismatch")
	}
	threshold, err := consensus.Threshold(c.genesis.Raw.HashBits, c.genesis.BaseTarget, winner.CTicket)
	if err != nil || !consensus.TicketWins(winner.TicketHash, threshold) {
		return errors.New("winner ticket does not meet target")
	}
	if c.genesis.Raw.Model.AcceptsRandomProof() {
		if len(winner.Certificate) == 0 {
			return errors.New("dummy proof is empty")
		}
	} else if c.genesis.Raw.Model.UsesProofBundle() {
		if len(winner.ProofBundle) == 0 {
			return errors.New("winner proof bundle is empty")
		}
		if len(winner.Certificate) != 0 {
			return errors.New("auxiliary winner still carries a legacy certificate")
		}
		if _, err := proofutil.VerifyBundle(winner.ProofBundle, winner.ProofProfile, winner.ProofSetupDigest, codec.WinnerStatement(block), c.genesis.CertificatePublic); err != nil {
			return fmt.Errorf("winner proof bundle: %w", err)
		}
	} else if err := cryptoutil.Verify(c.genesis.CertificatePublic, winner.Certificate, "certificate", codec.WinnerStatement(block)); err != nil {
		return fmt.Errorf("winner certificate: %w", err)
	}
	return nil
}

func verifyTransaction(tx types.Tx, parent types.State, genesis profile.Genesis) error {
	if tx.Kind == types.TxResponse {
		miner, ok := genesis.Miners[tx.MinerID]
		if !ok || tx.Sender != tx.MinerID {
			return errors.New("response sender is not its miner")
		}
		return cryptoutil.Verify(miner.IdentityPub, tx.Signature, "transaction", codec.TxSigningBytes(tx))
	}
	account, ok := parent.Accounts[tx.Sender]
	if !ok {
		return errors.New("transaction sender is unknown")
	}
	return cryptoutil.Verify(account.Public, tx.Signature, "transaction", codec.TxSigningBytes(tx))
}

func queryContext(parent types.State, prefix []types.Tx, queryID string) (types.Query, bool) {
	if query, ok := parent.Queries[queryID]; ok {
		return query, true
	}
	for _, tx := range prefix {
		if tx.Kind == types.TxQuery && tx.QueryID == queryID {
			return types.Query{
				ID: tx.QueryID, Sender: tx.Sender, Input: tx.Input,
				Commitment: tx.Commitment, Recipient: tx.Recipient,
				ModelID: tx.ModelID, Fee: tx.Fee, Expiry: tx.Expiry,
			}, true
		}
	}
	return types.Query{}, false
}

func (c *Chain) shouldAdopt(candidate types.Hash, height uint64, arrival time.Time) bool {
	if c.tip == c.genesis.Hash {
		return true
	}
	if height > c.tipHeight {
		return true
	}
	if height < c.tipHeight {
		return false
	}
	oldArrival, ok := c.arrival[c.tip]
	return ok && arrival.Before(oldArrival)
}

func (c *Chain) emit(eventType, status, reason string, block types.Block, id types.Hash) {
	if c.events == nil {
		return
	}
	_ = c.events.Emit(eventType, status, reason, map[string]any{
		"height": block.Header.Height, "block_hash": id.String(),
		"parent_hash": block.Header.Parent.String(), "miner_id": block.Header.Producer,
	})
}

func (c *Chain) CanonicalBlocks(fromHeight, limit uint64) ([]types.Block, error) {
	c.mu.RLock()
	tip := c.tip
	c.mu.RUnlock()
	var reverse []types.Block
	for tip != c.genesis.Hash && uint64(len(reverse)) < fromHeight+limit {
		block, err := c.store.Block(tip)
		if err != nil {
			return nil, err
		}
		reverse = append(reverse, block)
		tip = block.Header.Parent
	}
	blocks := make([]types.Block, 0, len(reverse))
	for i := len(reverse) - 1; i >= 0; i-- {
		if reverse[i].Header.Height >= fromHeight {
			blocks = append(blocks, reverse[i])
		}
	}
	if uint64(len(blocks)) > limit {
		blocks = blocks[:limit]
	}
	return blocks, nil
}
