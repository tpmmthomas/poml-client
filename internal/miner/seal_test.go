// These tests control result delivery order independently of goroutine scheduling.
package miner

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	pb "github.com/thomas/poml-node/api/proto"
	"github.com/thomas/poml-node/profiles"

	"github.com/thomas/poml-node/internal/codec"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/internal/worker"
)

type overlappingWorker struct {
	*testBackend
	secondStarted chan struct{}
}

func (b *overlappingWorker) Infer(ctx context.Context, r worker.InferenceRequest) (worker.InferenceResult, error) {
	result, err := b.testBackend.Infer(ctx, r)
	// Both jobs finish local inference. The second result becomes observable
	// only after sealing has canceled outstanding calls.
	if r.AttemptID == "miner-1" {
		<-b.secondStarted
	} else {
		close(b.secondStarted)
		<-ctx.Done()
	}
	return result, err
}

func TestFirstHitDiscardsLaterSameQueryResponse(t *testing.T) {
	m, r, b, _ := testRound(t)
	for n := byte(1); ; n++ {
		r.parent = types.HashBytes("overlap-parent", []byte{n})
		r.digest = codec.PrefixDigest(r.parent, r.prefix)
		first := m.attempt(t.Context(), r.parent, r.digest, r.queries[0], "miner-1", "calibrate")
		second := m.attempt(t.Context(), r.parent, r.digest, r.queries[0], "miner-2", "calibrate")
		if first.err != nil || second.err != nil {
			t.Fatal(first.err, second.err)
		}
		if bytes.Compare(first.candidate.TicketHash[:], second.candidate.TicketHash[:]) < 0 {
			m.config.Genesis.BaseTarget = new(big.Int).Add(new(big.Int).SetBytes(first.candidate.TicketHash[:]), big.NewInt(1))
			break
		}
		if n == 255 {
			t.Fatal("could not construct overlapping fixture")
		}
	}
	m.config.Parallelism = 2
	m.config.Worker = &overlappingWorker{testBackend: b, secondStarted: make(chan struct{})}
	var published types.Block
	var queued []types.Tx
	m.config.PoolResponse = func(tx types.Tx) error { queued = append(queued, tx); return nil }
	m.config.SubmitBlock = func(block types.Block) error { published = block; return nil }
	if err := m.mineRound(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if published.Winner.AttemptID != "miner-1" || len(published.Responses) != 0 || len(queued) != 0 {
		t.Fatal("a second response for the winning query was retained")
	}
}

func TestDifferentQueriesCanFillTheResponseSuffix(t *testing.T) {
	m, r, _, _ := testRound(t)
	m.config.Genesis.Raw.ResponseLimit = 2
	s := observedResults{}
	for _, queryID := range []string{"q1", "q2"} {
		result := attemptResult{candidate: types.Winner{
			QueryID: queryID, AttemptID: "loss-" + queryID, MinerID: m.config.MinerID,
			Parent: r.parent, PrefixDigest: r.digest, ModelID: m.config.Model.ID,
			TicketHash: types.HashBytes("ticket", []byte(queryID)), CTicket: 1,
		}}
		if stop, err := m.observe(r, &s, result, false); stop || err != nil {
			t.Fatalf("loss %s stopped observation: stop=%v err=%v", queryID, stop, err)
		}
	}
	if len(s.losses) != 2 {
		t.Fatalf("different queries should produce two suffix responses, got %d", len(s.losses))
	}
	duplicate := attemptResult{candidate: types.Winner{
		QueryID: "q1", AttemptID: "loss-q1-duplicate", MinerID: m.config.MinerID,
		Parent: r.parent, PrefixDigest: r.digest, ModelID: m.config.Model.ID,
		TicketHash: types.HashBytes("ticket", []byte("duplicate")), CTicket: 1,
	}}
	if stop, err := m.observe(r, &s, duplicate, false); stop || err != nil {
		t.Fatalf("duplicate response changed observation: stop=%v err=%v", stop, err)
	}
	if len(s.losses) != 2 {
		t.Fatalf("duplicate query response was retained")
	}
}

func TestFitReservesFinalSignatureBytes(t *testing.T) {
	m, r, _, _ := testRound(t)
	candidate := m.attempt(t.Context(), r.parent, r.digest, r.queries[0], "miner-1", "calibrate").candidate
	b := types.Block{Prefix: r.prefix, Winner: candidate}
	tx := m.response(1, 1, candidate)
	full := b
	full.Signature = make([]byte, 64)
	full.Winner.Certificate = make([]byte, 64)
	full.Responses = []types.Tx{tx}
	limit := len(codec.BlockBytes(full))
	m.config.CheckLimits = func(block types.Block) error {
		if len(codec.BlockBytes(block)) > limit {
			return errors.New("byte limit")
		}
		return nil
	}
	if count, err := m.fitResponses(b, []types.Tx{tx, tx}); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	limit--
	if count, err := m.fitResponses(b, []types.Tx{tx}); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestFailedOverflowWriteIsRetried(t *testing.T) {
	m, r, _, _ := testRound(t)
	result := m.attempt(t.Context(), r.parent, r.digest, r.queries[0], "miner-8", "test")
	tx := m.response(1, 1, result.candidate)
	m.config.PoolResponse = func(types.Tx) error { return errors.New("storage unavailable") }
	m.queue(tx, "overflow")
	if len(m.unsaved) != 1 {
		t.Fatal("failed storage write lost the response")
	}
	var stored types.Hash
	m.config.PoolResponse = func(retried types.Tx) error { stored = codec.TxID(retried); return nil }
	m.retryQueue()
	if len(m.unsaved) != 0 || stored != codec.TxID(tx) {
		t.Fatal("retry changed the response or retained it after a durable write")
	}
}

type slowProof struct {
	*testBackend
	started, canceled chan struct{}
}

func (b *slowProof) Certify(ctx context.Context, _ profiles.Spec, _, _ string, _ []byte, _ *pb.Witness) ([]byte, worker.Timing, error) {
	close(b.started)
	<-ctx.Done()
	close(b.canceled)
	return nil, worker.Timing{}, ctx.Err()
}

func TestReplacedParentInterruptsCertification(t *testing.T) {
	m, r, b, _ := testRound(t)
	m.config.Genesis.BaseTarget = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	backend := &slowProof{testBackend: b, started: make(chan struct{}), canceled: make(chan struct{})}
	m.config.Worker = backend
	s := types.NewState()
	s.Queries["q1"] = r.queries[0]
	s.Accounts["u"] = types.Account{Balance: 100}
	v := &changeableView{parent: r.parent, state: s}
	m.config.View = v
	m.config.Prefix = func() []types.Tx { return nil }
	m.config.SubmitBlock = func(types.Block) error { t.Error("published uncertified stale block"); return nil }
	m.Start()
	defer m.Stop()
	select {
	case <-backend.started:
	case <-time.After(2 * time.Second):
		t.Fatal("proof was never requested")
	}
	replacement := types.HashBytes("replacement")
	v.mu.Lock()
	v.parent = replacement
	v.state = types.NewState()
	v.mu.Unlock()
	m.NotifyTip(replacement)
	select {
	case <-backend.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("proof was never canceled")
	}
}

// A solver can supply its original private witness again after an optimistic claim is challenged.
func TestChallengeReplaysTheClaimedAttempt(t *testing.T) {
	m, r, backend, _ := testRound(t)
	q := r.queries[0]
	original := m.attempt(t.Context(), r.parent, r.digest, q, "miner-18", "mining")
	if original.err != nil {
		t.Fatal(original.err)
	}
	response := m.response(r.height, 1, original.candidate)
	statement := []byte("canonical claim statement supplied by the ledger")
	proof, err := m.CertifyChallenge(t.Context(), q, response, statement)
	if err != nil || cryptoutil.Verify(m.config.Genesis.CertificatePublic, proof, "certificate", statement) != nil {
		t.Fatal("challenge certification failed", err)
	}
	response.Ciphertext = append([]byte(nil), response.Ciphertext...)
	response.Ciphertext[0] ^= 1
	if _, err := m.CertifyChallenge(t.Context(), q, response, statement); err == nil || backend.proofs != 1 {
		t.Fatal("a changed claim obtained a new certificate")
	}
}
