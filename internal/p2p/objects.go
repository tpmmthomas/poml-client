// Object transfer checks lengths, IDs and ancestry before passing complete
// public bytes to the node. Peer hints alone never make an object valid.
package p2p

import (
	"errors"
	"fmt"
	"time"

	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/p2p/wire"
	"github.com/thomas/poml-node/internal/types"
)

func (p *peer) handleObject(f *wire.Frame) error {
	switch f.Kind {
	case wire.Frame_INV:
		return p.readInventory(f)
	case wire.Frame_GET_DATA, wire.Frame_NOT_FOUND:
		var r wire.ObjectRef
		if err := decode(f.Payload, &r); err != nil {
			return err
		}
		k, err := key(&r)
		if err != nil {
			return err
		}
		if f.Kind == wire.Frame_NOT_FOUND {
			delete(p.pending, k)
			if len(p.page) > 0 && k.category == wire.ObjectRef_BLOCK && types.Hash(p.page[0].Id) == k.id {
				p.page = nil
			}
			return nil
		}
		body, ok := p.server.lookup(k)
		if !ok {
			return p.send(wire.Frame_NOT_FOUND, &r)
		}
		if uint64(len(body)) > p.server.config.MaxBlockBytes {
			return errors.New("stored object exceeds transport limit")
		}
		return p.send(wire.Frame_DATA, &wire.Data{Object: &r, Body: body})
	case wire.Frame_DATA:
		var data wire.Data
		if err := decode(f.Payload, &data); err != nil {
			return err
		}
		k, err := key(data.Object)
		if err != nil {
			return err
		}
		_, pending := p.pending[k]
		delete(p.pending, k)
		if !pending && !(k.category == wire.ObjectRef_INPUT && p.server.wantsInput(k.id)) {
			return nil
		}
		if len(data.Body) == 0 || uint64(len(data.Body)) > p.server.config.MaxBlockBytes {
			return errors.New("invalid object size")
		}
		if k.category == wire.ObjectRef_BLOCK {
			// Measure the requested full body before decoding, disk lookups,
			// and any wait behind another peer's validation.
			p.server.emit("block_arrival", "received", "requested body reached peer reader",
				map[string]any{"block_hash": k.id.String(), "peer_id": p.remote.ID, "bytes": len(data.Body)})
		}
		return p.receive(k, data.Body)
	}
	return nil
}

func (p *peer) receive(k objectKey, body []byte) error {
	var parent types.Hash
	// Decode and recompute the consensus ID, irrespective of the ID supplied
	// in the remote inventory. Input bytes have their own commitment domain.
	var observed types.Hash
	switch k.category {
	case wire.ObjectRef_TX:
		tx, err := codec.DecodeTx(body)
		if err != nil {
			return err
		}
		observed = codec.TxID(tx)
	case wire.ObjectRef_BLOCK:
		block, err := codec.DecodeBlock(body)
		if err != nil {
			return err
		}
		parent = block.Header.Parent
		observed = codec.BlockID(block)
	case wire.ObjectRef_INPUT:
		observed = types.HashBytes("input", body)
	}
	if observed != k.id {
		return fmt.Errorf("%s body does not match advertised ID", k.category)
	}
	if k.category == wire.ObjectRef_BLOCK && !p.server.has(objectKey{wire.ObjectRef_BLOCK, parent}) {
		return p.missingParent(k.id, parent)
	}
	err := p.admitObject(k, body)
	if err != nil {
		if k.category == wire.ObjectRef_BLOCK {
			p.page = nil
		}
		return nil // a nonce or available balance may change between nodes
	}
	if k.category == wire.ObjectRef_BLOCK {
		children := p.children[k.id]
		delete(p.children, k.id)
		p.orphans -= len(children)
		for _, child := range children {
			if err := p.request(objectKey{wire.ObjectRef_BLOCK, child}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *peer) admitObject(k objectKey, body []byte) error {
	s := p.server
	s.admit.Lock()
	defer s.admit.Unlock()
	if s.has(k) {
		return nil
	}
	start := time.Now()
	name, field := "tx_validate", "tx_id"
	var err error
	switch k.category {
	case wire.ObjectRef_BLOCK:
		name, field = "block_validate", "block_hash"
		err = s.callbacks.AcceptBlock(body)
	case wire.ObjectRef_TX:
		err = s.callbacks.AcceptTx(body)
	case wire.ObjectRef_INPUT:
		name, field = "input_validate", "commitment"
		if s.callbacks.StoreInput == nil {
			err = errors.New("input store unavailable")
		} else {
			err = s.callbacks.StoreInput(k.id, body)
		}
	}
	fields := map[string]any{field: k.id.String(), "peer_id": p.remote.ID, "bytes": len(body), "wall_ns": time.Since(start).Nanoseconds()}
	if err != nil {
		s.emit(name, "rejected", err.Error(), fields)
		return err
	}
	s.emit(name, "accepted", "object passed local checks", fields)
	s.announce(k, p)
	return nil
}

func (p *peer) missingParent(child, parent types.Hash) error {
	for _, id := range p.children[parent] {
		if id == child {
			return nil
		}
	}
	if p.orphans >= maxInventory {
		return errors.New("too many unresolved ancestor requests")
	}
	p.children[parent] = append(p.children[parent], child)
	p.orphans++
	// Keep only the child ID. Request its body again after the parent passes
	// local validation, so a remote peer cannot fill memory with orphan blocks.
	return p.request(objectKey{wire.ObjectRef_BLOCK, parent})
}
