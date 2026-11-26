// Package miner manages an honest node's local attempt scheduler and first-hit seal.
package miner

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"sync"
	"time"

	pb "github.com/thomas/poml-node/api/proto"
	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/events"
	"github.com/thomas/poml-node/internal/profile"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/internal/worker"
	"github.com/thomas/poml-node/profiles"
)

// View provides an immutable snapshot for an identified parent. Fetching by
// hash keeps the freeze consistent if a peer supplies a block during startup.
type View interface {
	Tip() (types.Hash, uint64)
	StateAt(types.Hash) (types.State, error)
}

// Backend allows the same consensus node to talk to a different inference/proof process.
type Backend interface {
	Infer(context.Context, worker.InferenceRequest) (worker.InferenceResult, error)
	Certify(context.Context, profiles.Spec, string, string, []byte, *pb.Witness) ([]byte, worker.Timing, error)
}

// Config supplies the local keys, durable operations and shared block validator.
type Config struct {
	MinerID                       string
	Identity                      ed25519.PrivateKey
	InferenceSeed, EncryptionSeed []byte
	ProfileID                     string
	Model                         profiles.Spec
	MaxAttempts                   uint64
	Parallelism                   int
	Genesis                       profile.Genesis
	View                          View
	Worker                        Backend
	Prefix                        func() []types.Tx
	SubmitBlock                   func(types.Block) error
	Event                         *events.Writer
	OnBlock                       func(types.Block)
	NextAttempt                   func() (uint64, error)
	PoolResponse                  func(types.Tx) error
	CheckLimits                   func(types.Block) error
}

// Miner owns only local scheduling. The chain independently validates its output.
type Miner struct {
	config      Config
	life        sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	roundMu     sync.Mutex
	parent      types.Hash
	cancelRound context.CancelFunc
	// Accessed only by the serial scheduler. Keep failed durable writes for retry.
	unsaved   map[types.Hash]types.Tx
	nextRetry time.Time
}

func New(config Config) *Miner {
	if config.Model.ID == "" {
		config.Model, _ = profiles.Fixture(config.ProfileID)
	}
	if config.Parallelism < 1 {
		config.Parallelism = 1
	}
	return &Miner{config: config, unsaved: make(map[types.Hash]types.Tx)}
}

// Start may be called repeatedly. Stop joins all attempts before returning.
func (m *Miner) Start() {
	m.life.Lock()
	defer m.life.Unlock()
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	go func() { defer close(m.done); m.loop(ctx) }()
}

func (m *Miner) Stop() {
	m.life.Lock()
	defer m.life.Unlock()
	if m.cancel == nil {
		return
	}
	m.cancel()
	<-m.done
	m.cancel = nil
}

func (m *Miner) Running() bool { m.life.Lock(); defer m.life.Unlock(); return m.cancel != nil }

// NotifyTip interrupts worker IO when another branch becomes canonical. It never
// waits for a job and can also run during local block submission.
func (m *Miner) NotifyTip(tip types.Hash) {
	m.roundMu.Lock()
	defer m.roundMu.Unlock()
	if m.cancelRound != nil && tip != m.parent {
		m.cancelRound()
	}
}

func (m *Miner) loop(ctx context.Context) {
	for ctx.Err() == nil {
		m.retryQueue()
		parent, height := m.config.View.Tip()
		roundCtx, cancel := context.WithCancel(ctx)
		m.roundMu.Lock()
		m.parent, m.cancelRound = parent, cancel
		m.roundMu.Unlock()
		tip, _ := m.config.View.Tip()
		if tip != parent {
			cancel()
			continue
		}
		err := m.runParent(roundCtx, parent, height)
		cancel()
		m.roundMu.Lock()
		m.cancelRound = nil
		m.roundMu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			kind := "mining_error"
			if errors.Is(err, errNoDemand) {
				kind = "no_demand"
			}
			status := "failed"
			if kind == "no_demand" {
				status = "idle"
			}
			m.record(kind, status, err.Error(), map[string]any{"parent_hash": parent.String()})
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}

var errNoDemand = errors.New("no eligible retrievable query")

func (m *Miner) runParent(ctx context.Context, parent types.Hash, height uint64) error {
	if err := m.config.Model.Validate(); err != nil {
		return err
	}
	current, err := m.config.View.StateAt(parent)
	if err != nil {
		return err
	}
	prefix := m.config.Prefix()
	candidates, err := eligibleQueries(current, prefix, height+1, m.config.Genesis)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return errNoDemand
	}
	round := miningRound{parent: parent, height: height + 1, prefix: prefix, digest: codec.PrefixDigest(parent, prefix), queries: candidates}
	txIDs := make([]string, len(prefix))
	for i, tx := range prefix {
		txIDs[i] = codec.TxID(tx).String()
	}
	m.record("prefix_freeze", "complete", "ordered prefix fixed", map[string]any{
		"parent_hash": parent.String(), "prefix_digest": round.digest.String(), "tx_ids": txIDs,
		"query_count": len(candidates),
	})
	return m.mineRound(ctx, round)
}

func (m *Miner) record(event, status, reason string, fields map[string]any) {
	if m.config.Event != nil {
		fields["miner_id"], fields["synthetic_profile_id"] = m.config.MinerID, m.config.Model.ID
		if err := m.config.Event.Emit(event, status, reason, fields); err != nil {
			slog.Error("record mining event", "event", event, "error", err)
		}
	}
}
