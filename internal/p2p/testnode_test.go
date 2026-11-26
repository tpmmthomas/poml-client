// Test nodes use real loopback sockets with a small in-memory block DAG. The
// DAG deliberately accepts simple fixture bodies so these tests isolate wire
// ordering, request handling and synchronization from the consensus suite.
package p2p

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/types"
)

var testGenesis = types.HashBytes("p2p-test-genesis")

type testLedger struct {
	mu         sync.Mutex
	tip        types.Hash
	height     uint64
	blocks     map[types.Hash][]byte
	txs        map[types.Hash][]byte
	inputs     map[types.Hash][]byte
	acceptedTx map[types.Hash]int
}

func ledger() *testLedger {
	return &testLedger{tip: testGenesis, blocks: map[types.Hash][]byte{}, txs: map[types.Hash][]byte{}, inputs: map[types.Hash][]byte{}, acceptedTx: map[types.Hash]int{}}
}

func (l *testLedger) tipAt() (types.Hash, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tip, l.height
}

func (l *testLedger) acceptBlock(data []byte) error {
	b, err := codec.DecodeBlock(data)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b.Header.Parent != testGenesis && l.blocks[b.Header.Parent] == nil {
		return errors.New("parent not present")
	}
	id := codec.BlockID(b)
	l.blocks[id] = data
	if b.Header.Height > l.height {
		l.height = b.Header.Height
		l.tip = id
	}
	return nil
}

func (l *testLedger) appendBlocks(t *testing.T, count int, producer string) []byte {
	t.Helper()
	var last []byte
	for range count {
		parent, height := l.tipAt()
		last = codec.BlockBytes(types.Block{Header: types.Header{Height: height + 1, Parent: parent, Producer: producer}})
		if err := l.acceptBlock(last); err != nil {
			t.Fatal(err)
		}
	}
	return last
}

func (l *testLedger) callbacks() Callbacks {
	values := func(values map[types.Hash][]byte) func(types.Hash) ([]byte, bool) {
		return func(id types.Hash) ([]byte, bool) {
			l.mu.Lock()
			defer l.mu.Unlock()
			v, ok := values[id]
			return v, ok
		}
	}
	return Callbacks{
		Tip: l.tipAt, AcceptBlock: l.acceptBlock,
		AcceptTx: func(data []byte) error {
			tx, err := codec.DecodeTx(data)
			if err != nil {
				return err
			}
			if tx.Sender == "reject" {
				return errors.New("ineligible test transaction")
			}
			id := codec.TxID(tx)
			l.mu.Lock()
			defer l.mu.Unlock()
			l.txs[id] = data
			l.acceptedTx[id]++
			return nil
		},
		BlockBytes: values(l.blocks), TxBytes: values(l.txs), InputBytes: values(l.inputs),
		StoreInput: func(id types.Hash, data []byte) error {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.inputs[id] = data
			return nil
		},
		PendingTxIDs: func() []types.Hash {
			l.mu.Lock()
			defer l.mu.Unlock()
			var ids []types.Hash
			for id := range l.txs {
				ids = append(ids, id)
			}
			return ids
		},
		CanonicalBlocks: func(from, limit uint64) ([][]byte, error) {
			l.mu.Lock()
			defer l.mu.Unlock()
			var reverse [][]byte
			id := l.tip
			for h := l.height; h >= from && h > 0; h-- {
				body := l.blocks[id]
				b, err := codec.DecodeBlock(body)
				if err != nil {
					return nil, err
				}
				if h-from < limit {
					reverse = append(reverse, body)
				}
				id = b.Header.Parent
			}
			result := make([][]byte, 0, len(reverse))
			for i := len(reverse) - 1; i >= 0; i-- {
				result = append(result, reverse[i])
			}
			return result, nil
		},
	}
}

func testConfig(t *testing.T, label string, seeds ...string) Config {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Config{NodeID: label, Listen: "127.0.0.1:0", AddressBook: filepath.Join(t.TempDir(), "peers.json"),
		Identity: private, IdentityPub: public, GenesisHash: testGenesis, ChainID: "test", ProfileID: "fixture",
		Seeds: seeds, MaxBlockBytes: 1 << 20}
}

type running struct {
	*Server
	cancel context.CancelFunc
	done   chan struct{}
}

func startTest(t *testing.T, config Config, l *testLedger) *running {
	t.Helper()
	s := New(config, l.callbacks())
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{s, cancel, make(chan struct{})}
	go func() {
		defer close(r.done)
		if err := s.Start(ctx); err != nil {
			t.Errorf("serve %s: %v", config.NodeID, err)
		}
	}()
	t.Cleanup(func() { r.stop(t) })
	return r
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("P2P shutdown did not join socket goroutines")
	}
}

func await(t *testing.T, description string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}
