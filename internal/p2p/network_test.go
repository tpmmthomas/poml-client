// Network tests check externally visible behavior across independent services.
package p2p

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/p2p/wire"
	"github.com/thomas/poml-node/internal/types"
)

func TestLocatorPagesAcrossExistingFork(t *testing.T) {
	producer := ledger()
	producer.appendBlocks(t, 2*pageSize+5, "producer")
	a := startTest(t, testConfig(t, "a"), producer)
	verifier := ledger()
	verifier.appendBlocks(t, 8, "prior-branch")
	old, _ := verifier.tipAt()
	b := startTest(t, testConfig(t, "b", a.config.Advertise), verifier)
	want, _ := producer.tipAt()
	await(t, "paginated synchronization", func() bool { tip, _ := verifier.tipAt(); return tip == want })
	if _, exists := b.callbacks.BlockBytes(old); !exists {
		t.Fatal("side branch lost during synchronization")
	}
	newTipBody := producer.appendBlocks(t, 3, "producer")
	a.BroadcastBlock(newTipBody, nil) // only announce the grandchild
	want, _ = producer.tipAt()
	await(t, "missing ancestor bodies", func() bool { tip, _ := verifier.tipAt(); return tip == want })
}

func TestDiscoveryInventoryInputsAndRestart(t *testing.T) {
	first, middle, last := ledger(), ledger(), ledger()
	a := startTest(t, testConfig(t, "a"), first)
	b := startTest(t, testConfig(t, "b", a.config.Advertise), middle)
	cfg := testConfig(t, "c", b.config.Advertise)
	c := startTest(t, cfg, last)
	await(t, "line seeds to discover all three peers", func() bool { return len(a.Peers()) == 2 && len(b.Peers()) == 2 && len(c.Peers()) == 2 })
	tx := types.Tx{Kind: types.TxTransfer, Sender: "origin", Amount: 1}
	body := codec.TxBytes(tx)
	id := codec.TxID(tx)
	if err := a.callbacks.AcceptTx(body); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		a.BroadcastTx(body, nil)
	}
	for _, l := range []*testLedger{middle, last} {
		await(t, "validated transaction", func() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.acceptedTx[id] == 1 })
	}
	input := []byte("input obtained from a peer")
	h := types.HashBytes("input", input)
	if err := a.callbacks.StoreInput(h, input); err != nil {
		t.Fatal(err)
	}
	c.RequestInput(h)
	await(t, "query input matching signed commitment", func() bool { v, _ := c.callbacks.InputBytes(h); return bytes.Equal(v, input) })
	// Two independent stops leave only A online. Restart C with its original
	// identity and disk address book, with its initial seed removed.
	c.stop(t)
	b.stop(t)
	cfg.Seeds = nil
	reconnected := startTest(t, cfg, last)
	await(t, "discovered endpoint after restart", func() bool { peers := reconnected.Peers(); return len(peers) == 1 && peers[0].Name == "a" })
	last.mu.Lock()
	count := last.acceptedTx[id]
	last.mu.Unlock()
	if count != 1 {
		t.Fatalf("transaction validated %d times", count)
	}
}

func TestGenesisMismatchNeverRegistersPeer(t *testing.T) {
	a := startTest(t, testConfig(t, "a"), ledger())
	config := testConfig(t, "other", a.config.Advertise)
	config.GenesisHash = types.HashBytes("other genesis")
	b := startTest(t, config, ledger())
	conn, err := net.DialTimeout("tcp", a.config.Advertise, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Normal handshakes publish HELLO first. A protocol mismatched client is
	// refused before any callback receives transaction or block bytes.
	p := &peer{conn: conn, server: b.Server}
	if err = p.handshake(a.config.Advertise); err == nil {
		t.Fatal("mismatched genesis accepted")
	}
	if len(a.Peers())+len(b.Peers()) != 0 {
		t.Fatal("incompatible peer registered")
	}
}

func TestFramingRejectsOversizeBeforeReadingBody(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { var n [4]byte; binary.BigEndian.PutUint32(n[:], controlLimit+1); _, _ = a.Write(n[:]) }()
	if _, err := readFrame(b, controlLimit); err == nil {
		t.Fatal("oversized control frame accepted")
	}
}

// A reconnecting peer fetches several mempool pages. Invalid objects are
// checked locally and omitted from the receiving node's public inventory.
func TestPagedMempoolWithIndependentValidation(t *testing.T) {
	source, recipient := ledger(), ledger()
	for nonce := 0; nonce < maxInventory*2+3; nonce++ {
		tx := types.Tx{Kind: types.TxTransfer, Sender: "origin", Nonce: uint64(nonce), Amount: 1}
		if err := source.callbacks().AcceptTx(codec.TxBytes(tx)); err != nil {
			t.Fatal(err)
		}
	}
	rejected := types.Tx{Kind: types.TxTransfer, Sender: "reject", Amount: 1}
	badID := codec.TxID(rejected)
	source.txs[badID] = codec.TxBytes(rejected)
	a := startTest(t, testConfig(t, "source"), source)
	b := startTest(t, testConfig(t, "recipient", a.config.Advertise), recipient)
	await(t, "all eligible mempool pages", func() bool { return len(b.callbacks.PendingTxIDs()) == maxInventory*2+3 })
	if _, ok := b.callbacks.TxBytes(badID); ok {
		t.Fatal("failed transaction admitted")
	}
	c := startTest(t, testConfig(t, "later", b.config.Advertise), ledger())
	a.stop(t)
	await(t, "relay of independently checked transactions", func() bool { return len(c.callbacks.PendingTxIDs()) == maxInventory*2+3 })
	if _, ok := c.callbacks.TxBytes(badID); ok {
		t.Fatal("rejected transaction relayed")
	}
}

// The stored key must match the content that arrived over the socket.
func TestInputCommitmentCheckedBeforeStorage(t *testing.T) {
	l := ledger()
	s := New(testConfig(t, "a"), l.callbacks())
	p := &peer{server: s}
	commit := types.HashBytes("input", []byte("original bytes"))
	err := p.receive(objectKey{wire.ObjectRef_INPUT, commit}, []byte("different bytes"))
	if err == nil {
		t.Fatal("accepted input from a different commitment")
	}
	if _, ok := s.callbacks.InputBytes(commit); ok {
		t.Fatal("stored inconsistent input")
	}
}
