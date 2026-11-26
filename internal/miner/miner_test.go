// Mining tests use complete deterministic worker requests and exercise the first-hit seal.
package miner

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/thomas/poml-node/api/proto"
	"github.com/thomas/poml-node/internal/codec"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/profile"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/internal/worker"
	"github.com/thomas/poml-node/profiles"
)

type testBackend struct {
	key             ed25519.PrivateKey
	proofs          int
	entered, exited chan struct{}
}

func (b *testBackend) Infer(ctx context.Context, r worker.InferenceRequest) (worker.InferenceResult, error) {
	if b.entered != nil {
		select {
		case b.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		select {
		case b.exited <- struct{}{}:
		default:
		}
		return worker.InferenceResult{}, ctx.Err()
	}
	if len(r.Randomness) != int(r.Spec.RandomnessSlots) || len(r.SignedQuery) == 0 || types.HashBytes("input", r.Input) != r.Commitment {
		return worker.InferenceResult{}, errors.New("incomplete request")
	}
	out := types.HashBytes("test-output", r.Input, r.Randomness[0])
	return worker.InferenceResult{Output: out[:], Witness: []byte("private handle")}, nil
}
func (b *testBackend) Certify(_ context.Context, _ profiles.Spec, _, purpose string, statement []byte, w *pb.Witness) ([]byte, worker.Timing, error) {
	if purpose == "" || len(w.EncryptionVrf) != 64 || len(w.EncryptionProof) != 80 || len(w.Randomness) == 0 || len(w.Execution) == 0 {
		return nil, worker.Timing{}, errors.New("missing proof inputs")
	}
	b.proofs++
	return cryptoutil.Sign(b.key, "certificate", statement), worker.Timing{}, nil
}

func testRound(t *testing.T) (*Miner, miningRound, *testBackend, *ecdh.PrivateKey) {
	t.Helper()
	s, _ := profiles.Fixture("fixture-a")
	g := profile.Genesis{
		Raw:        profile.GenesisFile{ChainID: "test-chain", ModelProfileID: s.ID, HashBits: 256, MaxBlockBytes: 1 << 20, ResponseLimit: 512},
		BaseTarget: big.NewInt(1),
	}
	k := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32))
	g.CertificatePublic = k.Public().(ed25519.PublicKey)
	recipient, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	tx := types.Tx{
		Kind: types.TxQuery, Sender: "u", QueryID: "q1", Input: []byte("test"),
		Recipient: recipient.PublicKey().Bytes(), Fee: 1, Expiry: 100, ModelID: s.ID,
	}
	tx.Commitment = types.HashBytes("input", tx.Input)
	tx.Signature = cryptoutil.Sign(k, "transaction", codec.TxSigningBytes(tx))
	q := types.Query{
		ID: tx.QueryID, Sender: tx.Sender, Input: tx.Input, Commitment: tx.Commitment,
		Recipient: tx.Recipient, ModelID: s.ID, Expiry: 100, SignedTx: codec.TxBytes(tx), Fee: 1,
	}
	r := miningRound{parent: types.HashBytes("genesis"), height: 1, prefix: []types.Tx{tx}, queries: []types.Query{q}}
	r.digest = codec.PrefixDigest(r.parent, r.prefix)
	b := &testBackend{key: k}
	var seq atomic.Uint64
	m := New(Config{
		Model: s, ProfileID: s.ID, MinerID: "miner", Identity: k, Genesis: g,
		InferenceSeed: bytes.Repeat([]byte{2}, 32), EncryptionSeed: bytes.Repeat([]byte{3}, 32),
		Parallelism: 1, MaxAttempts: 2, Worker: b,
		NextAttempt: func() (uint64, error) { return seq.Add(1), nil },
	})
	return m, r, b, recipient
}

func TestLongRunSealsOnceAndQueuesOverflow(t *testing.T) {
	for _, limit := range []uint64{2, 512} {
		t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
			m, r, backend, key := testRound(t)
			// Choose the lowest of the first 96 real tickets, giving an exact, repeatable loss streak.
			var least types.Hash
			var hit int
			for n := 1; n <= 96; n++ {
				result := m.attempt(t.Context(), r.parent, r.digest, r.queries[0], fmt.Sprintf("miner-%d", n), "calibrate")
				if result.err != nil {
					t.Fatal(result.err)
				}
				h := result.candidate.TicketHash
				if n == 1 || bytes.Compare(h[:], least[:]) < 0 {
					least, hit = h, n
				}
			}
			if hit <= 4 {
				t.Fatalf("fixture did not create a long enough streak, hit %d", hit)
			}
			m.config.Genesis.BaseTarget = new(big.Int).Add(new(big.Int).SetBytes(least[:]), big.NewInt(1))
			m.config.Genesis.Raw.ResponseLimit = limit
			var block types.Block
			var queued []types.Tx
			m.config.SubmitBlock = func(b types.Block) error { block = b; return nil }
			m.config.PoolResponse = func(tx types.Tx) error { queued = append(queued, tx); return nil }
			if err := m.mineRound(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			if backend.proofs != 1 || block.Winner.AttemptID != fmt.Sprintf("miner-%d", hit) || len(block.Responses) != 0 || len(queued) != 0 {
				t.Fatalf("hit %d, proofs %d, responses %d, overflow %d", hit, backend.proofs, len(block.Responses), len(queued))
			}
			if _, err := cryptoutil.OpenOutput(key.Bytes(), m.config.Genesis.Raw.ChainID, block.Winner); err != nil {
				t.Fatal(err)
			}
			if block.Winner.ProfileDigest != m.config.Model.Digest() || block.Winner.InputCommitment != r.queries[0].Commitment ||
				!bytes.Equal(block.Winner.Recipient, key.PublicKey().Bytes()) || len(block.Winner.InferenceVRFPublic) != 32 || len(block.Winner.EncryptionVRFPublic) != 32 {
				t.Fatal("winner statement omitted pinned public inputs")
			}
			before := block.Winner.TicketHash
			if err := cryptoutil.Verify(m.config.Genesis.CertificatePublic, block.Winner.Certificate, "certificate", codec.WinnerStatement(block)); err != nil {
				t.Fatal(err)
			}
			tampered := block
			tampered.Winner.Recipient = append([]byte(nil), block.Winner.Recipient...)
			tampered.Winner.Recipient[0] ^= 1
			if cryptoutil.Verify(m.config.Genesis.CertificatePublic, block.Winner.Certificate, "certificate", codec.WinnerStatement(tampered)) == nil {
				t.Fatal("changed encryption recipient retained a valid winner certificate")
			}
			if len(block.Responses) > 0 {
				block.Responses = block.Responses[:0]
				block.Header.ClaimDigest = codec.ClaimDigest(block.Responses)
				if cryptoutil.Verify(m.config.Genesis.CertificatePublic, block.Winner.Certificate, "certificate", codec.WinnerStatement(block)) == nil {
					t.Fatal("suffix mutation retained original certificate")
				}
			}
			if types.HashBytes("ticket", codec.TicketBytes(block.Winner)) != before {
				t.Fatal("suffix mutation changed ticket")
			}
		})
	}
}

type changeableView struct {
	mu     sync.Mutex
	parent types.Hash
	state  types.State
}

func (v *changeableView) Tip() (types.Hash, uint64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.parent, v.state.Height
}
func (v *changeableView) StateAt(_ types.Hash) (types.State, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.state.Copy(), nil
}

func TestAdoptedParentCancelsRunningInference(t *testing.T) {
	m, r, b, _ := testRound(t)
	b.entered = make(chan struct{}, 1)
	b.exited = make(chan struct{}, 1)
	initial := types.NewState()
	initial.Queries[r.queries[0].ID] = r.queries[0]
	initial.Accounts[r.queries[0].Sender] = types.Account{Balance: 100}
	v := &changeableView{parent: r.parent, state: initial}
	m.config.View = v
	m.config.Prefix = func() []types.Tx { return nil }
	m.Start()
	defer m.Stop()
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("inference did not start")
	}
	newParent := types.HashBytes("new-tip")
	v.mu.Lock()
	v.parent = newParent
	v.state = types.NewState()
	v.mu.Unlock()
	m.NotifyTip(newParent)
	select {
	case <-b.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("stale work continued")
	}
}

func TestAttemptReservationFailureStopsDispatch(t *testing.T) {
	m, r, _, _ := testRound(t)
	m.config.NextAttempt = func() (uint64, error) { return 0, errors.New("disk failure") }
	if err := m.mineRound(t.Context(), r); err == nil {
		t.Fatal("an attempt should never start from a fallback counter")
	}
}
