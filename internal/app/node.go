// Package app wires the independent node components into one daemon.
package app

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thomas/poml-node/internal/chain"
	"github.com/thomas/poml-node/internal/codec"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/events"
	"github.com/thomas/poml-node/internal/miner"
	"github.com/thomas/poml-node/internal/p2p"
	"github.com/thomas/poml-node/internal/profile"
	proofutil "github.com/thomas/poml-node/internal/proof"
	"github.com/thomas/poml-node/internal/rpc"
	"github.com/thomas/poml-node/internal/state"
	"github.com/thomas/poml-node/internal/store"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/internal/worker"
)

type Node struct {
	config   profile.NodeConfig
	genesis  profile.Genesis
	store    *store.Store
	chain    *chain.Chain
	events   *events.Writer
	p2p      *p2p.Server
	rpc      rpc.Service
	miner    *miner.Miner
	worker   worker.Client
	identity ed25519.PrivateKey
	mu       sync.Mutex
	mempool  map[types.Hash]types.Tx
	cancel   context.CancelFunc
	services sync.WaitGroup
}

func Open(configPath string) (*Node, error) {
	config, err := profile.LoadNodeConfig(configPath)
	if err != nil {
		return nil, err
	}
	baseDir := filepath.Dir(configPath)
	genesisPath := resolvePath(baseDir, config.Genesis)
	config.DataDir = resolvePath(baseDir, config.DataDir)
	genesis, err := profile.LoadGenesis(genesisPath)
	if err != nil {
		return nil, err
	}
	identityBytes, err := cryptoutil.DecodeHex(config.IdentityPrivateHex, ed25519.PrivateKeySize)
	if err != nil {
		return nil, fmt.Errorf("identity key: %w", err)
	}
	inferenceSeed, err := cryptoutil.DecodeHex(config.InferenceVRFHex, 32)
	if err != nil {
		return nil, fmt.Errorf("inference VRF seed: %w", err)
	}
	encryptionSeed, err := cryptoutil.DecodeHex(config.EncryptionVRFHex, 32)
	if err != nil {
		return nil, fmt.Errorf("encryption VRF seed: %w", err)
	}
	certificateKeyBytes, err := cryptoutil.DecodeHex(config.CertificatePrivHex, ed25519.PrivateKeySize)
	if config.CertificatePrivHex != "" {
		if err != nil {
			return nil, fmt.Errorf("certificate key: %w", err)
		}
		certificateKey := ed25519.PrivateKey(certificateKeyBytes)
		if !equalBytes(certificateKey.Public().(ed25519.PublicKey), genesis.CertificatePublic) {
			return nil, errors.New("certificate private key does not match genesis certificate key")
		}
	}
	identity := ed25519.PrivateKey(identityBytes)
	minerRecord, ok := genesis.Miners[config.MinerID]
	if !ok || !equalBytes(identity.Public().(ed25519.PublicKey), minerRecord.IdentityPub) {
		return nil, errors.New("node identity does not match its genesis miner record")
	}
	if !equalBytes(inferencePublic(inferenceSeed), minerRecord.InferenceVRFPub) {
		return nil, errors.New("inference VRF seed does not match genesis")
	}
	if !equalBytes(encryptionPublic(encryptionSeed), minerRecord.EncryptionVRFPub) {
		return nil, errors.New("encryption VRF seed does not match genesis")
	}
	if err := os.MkdirAll(config.DataDir, 0o755); err != nil {
		return nil, err
	}
	eventWriter, err := events.New(filepath.Join(config.DataDir, "events.jsonl"), map[string]any{
		"run_id": config.RunID, "node_id": config.NodeID, "software_revision": "dev",
		"genesis_hash": genesis.Hash.String(),
	})
	if err != nil {
		return nil, err
	}
	durable, err := store.Open(config.DataDir)
	if err != nil {
		_ = eventWriter.Close()
		return nil, err
	}
	var modelVerifier proofutil.ModelVerifier
	if strings.HasPrefix(genesis.Raw.Model.CertificateProfile, "zk-") {
		if len(config.ProofVerifierCommand) == 0 || config.ProofVerifierProfilesDir == "" || config.ProofVerifierCacheDir == "" {
			_ = durable.Close()
			_ = eventWriter.Close()
			return nil, errors.New("zk model profile requires proof_verifier_command")
		}
		modelVerifier = proofutil.ProcessVerifier{
			Command: config.ProofVerifierCommand, ModelProfileID: genesis.Raw.ModelProfileID,
			ProfilesDir: resolvePath(baseDir, config.ProofVerifierProfilesDir),
			CacheDir:    resolvePath(baseDir, config.ProofVerifierCacheDir),
			Timeout:     time.Duration(config.ProofVerifierTimeoutMS) * time.Millisecond,
		}
	}
	chainState, err := chain.Open(genesis, durable, eventWriter, modelVerifier)
	if err != nil {
		_ = durable.Close()
		_ = eventWriter.Close()
		return nil, err
	}
	node := &Node{
		config: config, genesis: genesis, store: durable, chain: chainState,
		events: eventWriter, identity: identity, mempool: make(map[types.Hash]types.Tx),
		worker: worker.Client{
			Socket:            resolvePath(baseDir, config.WorkerSocket),
			CertificateSocket: resolveOptionalPath(baseDir, config.CertificateSocket),
			// The model profile owns the runtime budget. Gemma can legitimately
			// exceed the Stage 1 fixture's ten-second test timeout.
			Timeout: 0,
		},
	}
	if persisted, loadErr := durable.Mempool(); loadErr == nil {
		for _, tx := range persisted {
			node.mempool[codec.TxID(tx)] = tx
		}
	} else {
		_ = chainState.Close()
		_ = durable.Close()
		_ = eventWriter.Close()
		return nil, fmt.Errorf("load mempool: %w", loadErr)
	}
	node.p2p = p2p.New(p2p.Config{
		NodeID: config.NodeID, Listen: config.Listen, Advertise: config.Advertise,
		Seeds: config.Seeds, AddressBook: filepath.Join(config.DataDir, "peers.json"),
		ChainID: genesis.Raw.ChainID, ProfileID: genesis.Raw.ModelProfileID,
		GenesisHash: genesis.Hash, Identity: identity, MaxBlockBytes: genesis.Raw.MaxBlockBytes,
		IdentityPub: identity.Public().(ed25519.PublicKey),
	}, p2p.Callbacks{
		Tip: node.chain.Tip, AcceptTx: node.acceptWireTx, AcceptBlock: node.acceptWireBlock,
		CanonicalBlocks: node.canonicalWireBlocks, BlockBytes: node.blockBytes,
		HasBlock: node.chain.HasBlock, HasTx: node.hasTx, TxBytes: node.txBytes,
		PendingTxIDs: node.pendingTxIDs, InputBytes: node.inputBytes,
		StoreInput: node.storeInput, Event: node.emit,
	})
	node.rpc = rpc.Service{
		Address: config.RPC, Token: config.RPCToken,
		NodeInfo: node.NodeInfo, Tip: node.chain.Tip, Block: node.chain.Block,
		SubmitTx: node.SubmitTx, StartMining: node.StartMining, StopMining: node.StopMining,
		EventsPath: filepath.Join(config.DataDir, "events.jsonl"),
	}
	node.miner = miner.New(miner.Config{
		MinerID: config.MinerID, Identity: identity, InferenceSeed: inferenceSeed,
		EncryptionSeed: encryptionSeed, ProfileID: genesis.Raw.ModelProfileID,
		MaxAttempts: config.MaxAttempts, Parallelism: config.AttemptParallelism,
		Genesis: genesis, View: node.chain, Worker: node.worker, Prefix: node.prefix,
		SubmitBlock: node.SubmitBlock, Event: eventWriter, OnBlock: node.broadcastBlock,
		PoolResponse: node.SubmitTx,
		NextAttempt:  func() (uint64, error) { return durable.NextAttempt(config.MinerID) },
	})
	return node, nil
}

func (n *Node) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	n.cancel = cancel
	if err := n.p2p.Listen(); err != nil {
		cancel()
		return fmt.Errorf("start P2P: %w", err)
	}
	n.services.Add(2)
	go func() {
		defer n.services.Done()
		if err := n.p2p.Start(ctx); err != nil && ctx.Err() == nil {
			n.emit("node_service_error", "failed", "P2P service stopped", map[string]any{"error": err.Error()})
		}
	}()
	go func() {
		defer n.services.Done()
		if err := n.rpc.Serve(ctx); err != nil && ctx.Err() == nil {
			n.emit("node_service_error", "failed", "RPC service stopped", map[string]any{"error": err.Error()})
		}
	}()
	if n.config.Mining {
		n.miner.Start()
	}
	_ = n.events.Emit("node_start", "accepted", "node services started", map[string]any{
		"listen": n.config.Listen, "rpc": n.config.RPC, "miner_id": n.config.MinerID,
	})
	return nil
}

func (n *Node) Close() error {
	if n.cancel != nil {
		n.cancel()
	}
	n.miner.Stop()
	n.services.Wait()
	_ = n.events.Emit("node_stop", "complete", "node services stopped", nil)
	if err := n.events.Close(); err != nil {
		return err
	}
	if err := n.chain.Close(); err != nil {
		return err
	}
	return n.store.Close()
}

func (n *Node) StartMining() error { n.miner.Start(); return nil }
func (n *Node) StopMining()        { n.miner.Stop() }

func (n *Node) SubmitTx(tx types.Tx) error {
	if err := n.validateMempoolTx(tx); err != nil {
		return err
	}
	id := codec.TxID(tx)
	n.mu.Lock()
	if _, exists := n.mempool[id]; exists {
		n.mu.Unlock()
		return nil
	}
	n.mempool[id] = tx
	n.mu.Unlock()
	if tx.Kind == types.TxQuery {
		if err := n.store.PutInput(tx.Commitment, tx.Input); err != nil {
			n.mu.Lock()
			delete(n.mempool, id)
			n.mu.Unlock()
			return err
		}
	}
	if err := n.store.PutMempool(tx); err != nil {
		n.mu.Lock()
		delete(n.mempool, id)
		n.mu.Unlock()
		return err
	}
	n.emit("tx_receive", "accepted", "transaction entered mempool", map[string]any{"tx_id": id.String(), "qid": tx.QueryID})
	if n.p2p != nil {
		n.p2p.BroadcastTx(codec.TxBytes(tx), nil)
	}
	return nil
}

func (n *Node) SubmitBlock(block types.Block) error {
	result, err := n.chain.AddBlock(block)
	if err != nil {
		return err
	}
	n.reconcileMempool(result)
	if result.Adopted {
		n.miner.NotifyTip(result.BlockID)
	}
	if result.Adopted {
		n.emit("block_publish", "accepted", "block became local canonical tip", map[string]any{"block_hash": result.BlockID.String(), "height": result.Height})
	}
	return nil
}

func (n *Node) acceptWireTx(data []byte) error {
	tx, err := codec.DecodeTx(data)
	if err != nil {
		return err
	}
	return n.SubmitTx(tx)
}

func (n *Node) acceptWireBlock(data []byte) error {
	block, err := codec.DecodeBlock(data)
	if err != nil {
		return err
	}
	result, err := n.chain.AddBlock(block)
	if err != nil {
		return err
	}
	n.reconcileMempool(result)
	if result.Adopted {
		n.miner.NotifyTip(result.BlockID)
	}
	n.emit("block_receive", "accepted", "valid block received", map[string]any{"block_hash": result.BlockID.String(), "height": result.Height})
	return nil
}

func (n *Node) broadcastBlock(block types.Block) {
	n.p2p.BroadcastBlock(codec.BlockBytes(block), nil)
}

func (n *Node) canonicalWireBlocks(from, limit uint64) ([][]byte, error) {
	blocks, err := n.chain.CanonicalBlocks(from, limit)
	if err != nil {
		return nil, err
	}
	encoded := make([][]byte, 0, len(blocks))
	for _, block := range blocks {
		encoded = append(encoded, codec.BlockBytes(block))
	}
	return encoded, nil
}

func (n *Node) blockBytes(id types.Hash) ([]byte, bool) {
	block, err := n.chain.Block(id)
	if err != nil {
		return nil, false
	}
	return codec.BlockBytes(block), true
}

func (n *Node) hasTx(id types.Hash) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.mempool[id]
	return ok
}

func (n *Node) txBytes(id types.Hash) ([]byte, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	tx, ok := n.mempool[id]
	if !ok {
		return nil, false
	}
	return codec.TxBytes(tx), true
}

func (n *Node) pendingTxIDs() []types.Hash {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := make([]types.Hash, 0, len(n.mempool))
	for id := range n.mempool {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

func (n *Node) inputBytes(commitment types.Hash) ([]byte, bool) {
	input, err := n.store.Input(commitment)
	return input, err == nil
}

func (n *Node) storeInput(commitment types.Hash, input []byte) error {
	return n.store.PutInput(commitment, input)
}

func (n *Node) prefix() []types.Tx {
	n.mu.Lock()
	defer n.mu.Unlock()
	parent, height := n.chain.Tip()
	current, err := n.chain.StateAt(parent)
	if err != nil {
		return nil
	}
	txs := make([]types.Tx, 0, len(n.mempool))
	for _, tx := range n.mempool {
		txs = append(txs, tx)
	}
	sort.Slice(txs, func(i, j int) bool { return codec.TxID(txs[i]).String() < codec.TxID(txs[j]).String() })
	result := make([]types.Tx, 0, len(txs))
	bytesUsed := uint64(0)
	maxBytes := n.genesis.Raw.MaxBlockBytes / 2
	for _, tx := range txs {
		if n.genesis.Raw.MaxOrdinaryTxs > 0 && uint64(len(result)) >= n.genesis.Raw.MaxOrdinaryTxs {
			break
		}
		encoded := codec.TxBytes(tx)
		if bytesUsed+uint64(len(encoded)) > maxBytes {
			continue
		}
		candidate := current.Copy()
		if err := state.ApplyTransaction(candidate, tx, height+1, n.genesis); err != nil {
			continue
		}
		result = append(result, tx)
		bytesUsed += uint64(len(encoded))
		current = candidate
	}
	return result
}

func (n *Node) reconcileMempool(result chain.Result) {
	if !result.Adopted {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	attached := make(map[types.Hash]struct{}, len(result.AttachedTxs))
	for _, tx := range result.AttachedTxs {
		id := codec.TxID(tx)
		attached[id] = struct{}{}
		delete(n.mempool, id)
		_ = n.store.DeleteMempool(tx)
	}
	for _, tx := range result.DetachedTxs {
		id := codec.TxID(tx)
		if _, replaced := attached[id]; replaced {
			continue
		}
		if _, exists := n.mempool[id]; exists {
			continue
		}
		n.mempool[id] = tx
		if err := n.store.PutMempool(tx); err != nil {
			n.emit("tx_reorg", "failed", "could not restore detached transaction", map[string]any{"tx_id": id.String(), "error": err.Error()})
			continue
		}
		n.emit("tx_reorg", "accepted", "restored transaction from detached branch", map[string]any{"tx_id": id.String()})
	}
}

func (n *Node) validateMempoolTx(tx types.Tx) error {
	if len(tx.Signature) == 0 {
		return errors.New("transaction is missing a signature")
	}
	if tx.Kind < types.TxQuery || tx.Kind > types.TxMinerRegistration {
		return errors.New("transaction kind is unknown")
	}
	current, err := n.chain.TipState()
	if err != nil {
		return err
	}
	if tx.Kind == types.TxResponse {
		miner, ok := n.genesis.Miners[tx.MinerID]
		if !ok || tx.Sender != tx.MinerID {
			return errors.New("response sender is not a registered miner")
		}
		if _, ok := current.Accounts[tx.Sender]; !ok {
			return errors.New("response sender account is unknown")
		}
		if err := cryptoutil.Verify(miner.IdentityPub, tx.Signature, "transaction", codec.TxSigningBytes(tx)); err != nil {
			return err
		}
	} else {
		account, ok := current.Accounts[tx.Sender]
		if !ok {
			return errors.New("transaction sender is unknown")
		}
		if tx.Nonce < account.Nonce {
			return errors.New("transaction nonce is already committed")
		}
		if err := cryptoutil.Verify(account.Public, tx.Signature, "transaction", codec.TxSigningBytes(tx)); err != nil {
			return err
		}
	}
	if tx.Kind == types.TxQuery {
		if expected := types.HashBytes("input", tx.Input); expected != tx.Commitment {
			return errors.New("query input commitment mismatch")
		}
		if tx.ModelID != n.genesis.Raw.ModelProfileID {
			return errors.New("query model profile is not active")
		}
		if _, exists := current.Queries[tx.QueryID]; exists {
			return errors.New("query is already committed")
		}
		if len(tx.Recipient) != 32 {
			return errors.New("query recipient key must be an X25519 public key")
		}
	}
	return nil
}

func (n *Node) NodeInfo() map[string]any {
	tip, height := n.chain.Tip()
	return map[string]any{
		"node_id": n.config.NodeID, "miner_id": n.config.MinerID,
		"listen": n.config.Listen, "rpc": n.config.RPC,
		"chain_id": n.genesis.Raw.ChainID, "genesis_hash": n.genesis.Hash.String(),
		"height": height, "tip": tip.String(),
		"mining": n.miner.Running(), "events": filepath.Join(n.config.DataDir, "events.jsonl"),
	}
}

func (n *Node) emit(eventType, status, reason string, fields map[string]any) {
	_ = n.events.Emit(eventType, status, reason, fields)
}

func resolvePath(base, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(base, value))
}

func resolveOptionalPath(base, value string) string {
	if value == "" {
		return ""
	}
	return resolvePath(base, value)
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func inferencePublic(seed []byte) []byte {
	_, _, public, _ := cryptoutil.VRFProve(seed, []byte("genesis-public-check"))
	return public
}

func encryptionPublic(seed []byte) []byte {
	_, _, public, _ := cryptoutil.VRFProve(seed, []byte("genesis-public-check"))
	return public
}
