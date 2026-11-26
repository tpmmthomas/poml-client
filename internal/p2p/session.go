// A session admits traffic only after a two-sided identity challenge. The
// reader owns request state. The writer owns socket writes after the handshake.
package p2p

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/thomas/poml-node/internal/p2p/wire"
	"github.com/thomas/poml-node/internal/types"
)

const capability = "inventory/1,headers/1,inputs/1"
const heartbeat = 2 * time.Second

type peer struct {
	server      *Server
	conn        net.Conn
	public      []byte
	remote      PeerInfo
	started     time.Time
	out         chan *wire.Frame
	queuedBytes atomic.Int64
	lastPing    atomic.Int64

	// Only the reader mutates requests, ancestry and page cursors.
	want           []objectKey
	pending        map[objectKey]time.Time
	children       map[types.Hash][]types.Hash
	orphans        int
	remoteTip      *wire.Point
	page           []*wire.Point
	waitingHeaders time.Time
	lastLocator    []*wire.Point
	poolSnapshot   []types.Hash
	poolLoaded     bool
	waitingPool    time.Time
	morePool       bool
}

func (s *Server) serve(ctx context.Context, conn net.Conn, dialed string) bool {
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	defer conn.Close()
	p := &peer{server: s, conn: conn, started: time.Now(), out: make(chan *wire.Frame, 64), pending: make(map[objectKey]time.Time), children: make(map[types.Hash][]types.Hash)}
	if err := p.handshake(dialed); err != nil {
		if ctx.Err() == nil {
			s.emit("peer_handshake_reject", "rejected", err.Error(), nil)
		}
		return false
	}
	if !s.register(p) {
		return true
	}
	defer s.unregister(p)
	s.emit("peer_connected", "accepted", "fresh two-sided signature verified", map[string]any{"peer_id": p.remote.ID, "peer_name": p.remote.Name, "address": p.remote.Address})
	done := make(chan struct{})
	stopClose()
	// Graceful peers receive GOODBYE. A blocked socket gets a hard close
	// after a short grace interval and cannot hold daemon shutdown open.
	stopGraceful := context.AfterFunc(ctx, func() {
		p.enqueue(raw(wire.Frame_GOODBYE, nil))
		select {
		case <-done:
		case <-time.After(250 * time.Millisecond):
			_ = conn.Close()
		}
	})
	defer stopGraceful()
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); p.writeLoop(done) }()
	err := p.readLoop()
	close(done)
	_ = conn.Close()
	<-writerDone
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		s.emit("peer_message_reject", "rejected", err.Error(), map[string]any{"peer_id": p.remote.ID})
		return false
	}
	return true
}

func (p *peer) readLoop() error {
	p.enqueue(raw(wire.Frame_GET_ADDR, nil))
	if err := p.sendStatus(); err != nil {
		return err
	}
	p.askMempool()
	rateStart := time.Now()
	var frames, wireBytes int
	for {
		if err := p.setReadDeadline(); err != nil {
			return err
		}
		f, err := readFrame(p.conn, max(p.server.config.MaxBlockBytes+maxOverhead, controlLimit))
		if err != nil {
			return err
		}
		if time.Since(rateStart) >= time.Second {
			rateStart = time.Now()
			frames = 0
			wireBytes = 0
		}
		frames++
		wireBytes += len(f.Payload)
		if frames > 2048 || wireBytes > max(32<<20, 4*int(p.server.config.MaxBlockBytes)) {
			return errors.New("peer exceeded message or byte rate")
		}
		if err := p.handle(f); err != nil {
			return err
		}
		if err := p.pump(); err != nil {
			return err
		}
	}
}

func (p *peer) writeLoop(done <-chan struct{}) {
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		var msg *wire.Frame
		select {
		case <-done:
			return
		case msg = <-p.out:
			p.queuedBytes.Add(-int64(len(msg.Payload) + maxOverhead))
		case <-ticker.C:
			// Periodic status retries synchronization after transient validation
			// or a peer changing branches in the middle of a page.
			nonce := time.Since(p.started).Nanoseconds()
			p.lastPing.Store(nonce)
			if err := writeFrame(p.conn, raw(wire.Frame_PING, binary.BigEndian.AppendUint64(nil, uint64(nonce)))); err != nil {
				_ = p.conn.Close()
				return
			}
			tip, height := p.server.callbacks.Tip()
			var err error
			msg, err = frame(wire.Frame_STATUS, point(tip, height))
			if err != nil {
				_ = p.conn.Close()
				return
			}
		}
		if err := writeFrame(p.conn, msg); err != nil || msg.Kind == wire.Frame_GOODBYE {
			_ = p.conn.Close()
			return
		}
	}
}

func (p *peer) handle(f *wire.Frame) error {
	switch f.Kind {
	case wire.Frame_STATUS:
		var status wire.Point
		if err := decode(f.Payload, &status); err != nil {
			return err
		}
		return p.onStatus(&status)
	case wire.Frame_GET_ADDR:
		if len(f.Payload) != 0 {
			return errors.New("invalid address request")
		}
		p.server.mu.Lock()
		addrs := append([]string{p.server.config.Advertise}, p.server.listAddresses()...)
		p.server.mu.Unlock()
		return p.send(wire.Frame_ADDR, &wire.Addresses{Address: addrs})
	case wire.Frame_ADDR:
		var addresses wire.Addresses
		if err := decode(f.Payload, &addresses); err != nil {
			return err
		}
		if len(addresses.Address) > maxAddresses+1 {
			return errors.New("too many advertised peers")
		}
		return p.server.learn(addresses.Address)
	case wire.Frame_GET_MEMPOOL:
		if len(f.Payload) != 0 {
			return errors.New("invalid mempool request")
		}
		return p.shareMempool()
	case wire.Frame_MEMPOOL:
		if p.waitingPool.IsZero() {
			return errors.New("unsolicited mempool page")
		}
		p.waitingPool = time.Time{}
		return p.readInventory(f)
	case wire.Frame_INV, wire.Frame_GET_DATA, wire.Frame_DATA, wire.Frame_NOT_FOUND:
		return p.handleObject(f)
	case wire.Frame_HEADERS, wire.Frame_GET_HEADERS:
		var points wire.Points
		if err := decode(f.Payload, &points); err != nil {
			return err
		}
		if f.Kind == wire.Frame_GET_HEADERS {
			return p.serveHeaders(points.Items)
		}
		return p.readHeaders(points.Items)
	case wire.Frame_PING, wire.Frame_PONG:
		if len(f.Payload) != 8 {
			return errors.New("invalid heartbeat nonce")
		}
		if f.Kind == wire.Frame_PING {
			p.enqueue(raw(wire.Frame_PONG, f.Payload))
		} else if sent := int64(binary.BigEndian.Uint64(f.Payload)); sent > 0 && p.lastPing.CompareAndSwap(sent, 0) {
			p.server.emit("peer_rtt", "complete", "heartbeat round trip", map[string]any{"peer_id": p.remote.ID, "wall_ns": time.Since(p.started).Nanoseconds() - sent})
		}
	case wire.Frame_GOODBYE:
		return io.EOF
	default:
		return fmt.Errorf("unexpected P2P message %s", f.Kind)
	}
	return nil
}

func (p *peer) sendStatus() error {
	tip, height := p.server.callbacks.Tip()
	return p.send(wire.Frame_STATUS, point(tip, height))
}

// The oldest outstanding request limits the next read, even when the peer
// keeps sending unrelated messages.
func (p *peer) setReadDeadline() error {
	now := time.Now()
	deadline := now.Add(4 * requestTimeout)
	limit := func(sent time.Time) {
		if due := sent.Add(requestTimeout); !sent.IsZero() && due.Before(deadline) {
			deadline = due
		}
	}
	limit(p.waitingPool)
	limit(p.waitingHeaders)
	for _, sent := range p.pending {
		limit(sent)
	}
	if !deadline.After(now) {
		return errors.New("P2P request timed out")
	}
	return p.conn.SetReadDeadline(deadline)
}
