// Framing bounds memory before decoding. A dedicated writer per peer keeps a
// slow socket off the block acceptance and transaction submission paths.
package p2p

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/thomas/poml-node/internal/p2p/wire"
	"google.golang.org/protobuf/proto"
)

const maxOverhead = 256 // protobuf envelopes, object ref and lengths

func frame(kind wire.Frame_Kind, value proto.Message) (*wire.Frame, error) {
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(value)
	return &wire.Frame{Kind: kind, Payload: payload}, err
}

func writeFrame(conn net.Conn, msg *wire.Frame) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	if len(data) > objectLimit+maxOverhead {
		return errors.New("outbound frame exceeds byte limit")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if err := conn.SetWriteDeadline(time.Now().Add(requestTimeout)); err != nil {
		return err
	}
	// Buffers.WriteTo handles short writes and TCP scatter/gather.
	parts := net.Buffers{header[:], data}
	_, err = parts.WriteTo(conn)
	return err
}

func readFrame(conn net.Conn, limit uint64) (*wire.Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || uint64(n) > limit {
		return nil, fmt.Errorf("P2P frame length %d outside limits", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, err
	}
	var msg wire.Frame
	if err := decode(data, &msg); err != nil {
		return nil, err
	}
	if msg.Kind != wire.Frame_DATA && len(msg.Payload) > controlLimit {
		return nil, errors.New("oversized P2P control message")
	}
	return &msg, nil
}

func decode(data []byte, value proto.Message) error {
	if err := (proto.UnmarshalOptions{RecursionLimit: 8}).Unmarshal(data, value); err != nil {
		return err
	}
	if len(value.ProtoReflect().GetUnknown()) != 0 {
		return errors.New("unknown fields in P2P message")
	}
	return nil
}

// enqueue never waits for I/O. Closing a slow peer triggers its regular
// reconnect and inventory catch-up path without dropping arbitrary frames.
func (p *peer) enqueue(frame *wire.Frame) {
	size := int64(len(frame.Payload) + maxOverhead)
	if p.queuedBytes.Add(size) <= int64(p.server.config.MaxBlockBytes)*2+controlLimit {
		select {
		case p.out <- frame:
			return
		default:
		}
	}
	p.queuedBytes.Add(-size)
	_ = p.conn.Close()
}

func (p *peer) send(kind wire.Frame_Kind, value proto.Message) error {
	msg, err := frame(kind, value)
	if err == nil {
		p.enqueue(msg)
	}
	return err
}

func expect(conn net.Conn, kind wire.Frame_Kind) ([]byte, error) {
	f, err := readFrame(conn, controlLimit)
	if err != nil {
		return nil, err
	}
	if f.Kind != kind {
		return nil, fmt.Errorf("expected %s during handshake", kind)
	}
	return f.Payload, nil
}

func raw(kind wire.Frame_Kind, data []byte) *wire.Frame {
	return &wire.Frame{Kind: kind, Payload: data}
}

// An ACK proves live possession of the key for this pair of exact challenges.
// Both signed roles use the same sender-then-recipient transcript convention.
func transcript(sender, receiver []byte) []byte {
	var buf bytes.Buffer
	for _, msg := range [][]byte{sender, receiver} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(msg)))
		buf.Write(length[:])
		buf.Write(msg)
	}
	return buf.Bytes()
}
