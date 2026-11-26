// Package p2p connects independent nodes over authenticated TCP. It transports
// public objects and delegates their validity and fork choice to the node.
package p2p

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/thomas/poml-node/internal/p2p/wire"
	"github.com/thomas/poml-node/internal/types"
)

// Callbacks read the local durable block store, current mempool and input
// cache. Admission must finish before returning success or relaying an object.
type Callbacks struct {
	Tip             func() (types.Hash, uint64)
	AcceptTx        func([]byte) error
	AcceptBlock     func([]byte) error
	CanonicalBlocks func(uint64, uint64) ([][]byte, error)
	BlockBytes      func(types.Hash) ([]byte, bool)
	HasBlock        func(types.Hash) bool // optional cheap index membership
	HasTx           func(types.Hash) bool
	TxBytes         func(types.Hash) ([]byte, bool)
	PendingTxIDs    func() []types.Hash
	InputBytes      func(types.Hash) ([]byte, bool)
	StoreInput      func(types.Hash, []byte) error
	Event           func(string, string, string, map[string]any)
}

// Config separates per-host addresses and labels from shared network identity.
type Config struct {
	NodeID, Listen, Advertise, AddressBook string
	ChainID, ProfileID                     string
	Seeds                                  []string
	GenesisHash                            types.Hash
	Identity                               ed25519.PrivateKey
	IdentityPub                            ed25519.PublicKey
	MaxBlockBytes                          uint64
}

// PeerInfo is a snapshot for operator RPC. The ID is derived from the key
// which signed the handshake. The human-readable name is a peer-supplied label.
type PeerInfo struct {
	ID       string `json:"peer_id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Outbound bool   `json:"outbound"`
}

// Server owns socket lifetimes and a bounded address book. Peer-specific
// requests and sync progress belong to the reader for that connection.
type Server struct {
	config       Config
	callbacks    Callbacks
	mu           sync.Mutex
	listener     net.Listener
	peers        map[string]*peer
	addresses    map[string]*dialState
	wantedInputs map[types.Hash]time.Time
	slots        chan struct{}
	sessions     sync.WaitGroup
	admit        sync.Mutex // serialize check-and-admit across overlapping peer inventories
}

const (
	maxPeers       = 32
	maxAddresses   = 128
	maxInventory   = 128
	pageSize       = 16
	maxLocator     = 72
	controlLimit   = 64 << 10
	objectLimit    = 16 << 20
	requestTimeout = 8 * time.Second
)

// New constructs a stopped peer manager. Listen or Start opens sockets.
func New(config Config, callbacks Callbacks) *Server {
	return &Server{config: config, callbacks: callbacks, peers: make(map[string]*peer),
		addresses: make(map[string]*dialState), wantedInputs: make(map[types.Hash]time.Time), slots: make(chan struct{}, maxPeers)}
}

// Listen binds before a daemon reports readiness. Start also calls it when
// callers do not need a separate bind check.
func (s *Server) Listen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return nil
	}
	if s.callbacks.Tip == nil || s.callbacks.AcceptTx == nil || s.callbacks.AcceptBlock == nil || s.callbacks.CanonicalBlocks == nil || s.callbacks.BlockBytes == nil || s.callbacks.TxBytes == nil {
		return errors.New("P2P requires local chain, mempool and admission callbacks")
	}
	if len(s.config.Identity) != ed25519.PrivateKeySize || !bytes.Equal(s.config.Identity.Public().(ed25519.PublicKey), s.config.IdentityPub) {
		return errors.New("P2P identity key mismatch")
	}
	if s.config.MaxBlockBytes > objectLimit {
		return errors.New("configured block size exceeds P2P object limit")
	}
	if s.config.MaxBlockBytes == 0 {
		s.config.MaxBlockBytes = objectLimit
	}
	ln, err := net.Listen("tcp", s.config.Listen)
	if err != nil {
		return err
	}
	if _, port, _ := net.SplitHostPort(s.config.Advertise); s.config.Advertise == "" || port == "0" {
		s.config.Advertise = ln.Addr().String()
	}
	if !validAddress(s.config.Advertise) {
		_ = ln.Close()
		return errors.New("invalid P2P advertised address")
	}
	if err := s.loadAddresses(); err != nil {
		_ = ln.Close()
		return err
	}
	s.listener = ln
	return nil
}

// Start blocks until all sockets, dialers, readers and writers have stopped.
func (s *Server) Start(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	ln := s.listener
	done := context.AfterFunc(ctx, func() { _ = ln.Close() })
	dialDone := make(chan struct{})
	go func() { defer close(dialDone); s.dialLoop(ctx) }()
	var serveErr error
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				serveErr = err
			}
			break
		}
		select {
		case s.slots <- struct{}{}:
			s.sessions.Add(1)
			go func() { defer s.release(); s.serve(ctx, conn, "") }()
		default:
			_ = conn.Close()
		}
	}
	cancel()
	<-dialDone
	s.sessions.Wait()
	done()
	return serveErr
}

func (s *Server) release() {
	<-s.slots
	s.sessions.Done()
}

func (s *Server) register(p *peer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.peers[p.remote.ID]
	if old != nil {
		// If both ends dial at once, both retain the socket initiated by the
		// lower public key. Existing connections win all other ties.
		preferOutbound := bytes.Compare(s.config.IdentityPub, p.public) < 0
		if p.remote.Outbound != preferOutbound || old.remote.Outbound == preferOutbound {
			return false
		}
		_ = old.conn.Close()
	}
	s.peers[p.remote.ID] = p
	return true
}

func (s *Server) unregister(p *peer) {
	s.mu.Lock()
	if s.peers[p.remote.ID] == p {
		delete(s.peers, p.remote.ID)
	}
	s.mu.Unlock()
	s.emit("peer_disconnected", "complete", "connection closed", map[string]any{"peer_id": p.remote.ID})
}

// Peers returns a stable, public snapshot of authenticated sessions.
func (s *Server) Peers() []PeerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]PeerInfo, 0, len(s.peers))
	for _, p := range s.peers {
		result = append(result, p.remote)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Server) broadcast(frame *wire.Frame, skip *peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if p != skip {
			p.enqueue(frame)
		}
	}
}

func (s *Server) emit(kind, status, reason string, fields map[string]any) {
	if s.callbacks.Event != nil {
		s.callbacks.Event(kind, status, reason, fields)
	}
}
