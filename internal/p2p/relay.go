// Inventory announces short IDs. A peer explicitly asks for each missing
// transaction, block or input. Only validated objects are announced onward.
package p2p

import (
	"errors"
	"time"

	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/p2p/wire"
	"github.com/thomas/poml-node/internal/types"
)

type objectKey struct {
	category wire.ObjectRef_Category
	id       types.Hash
}

func ref(kind wire.ObjectRef_Category, id types.Hash) *wire.ObjectRef {
	return &wire.ObjectRef{Category: kind, Id: id[:]}
}

func key(r *wire.ObjectRef) (objectKey, error) {
	if r == nil || len(r.Id) != types.HashSize || r.Category < wire.ObjectRef_TX || r.Category > wire.ObjectRef_INPUT {
		return objectKey{}, errors.New("invalid inventory reference")
	}
	return objectKey{r.Category, types.Hash(r.Id)}, nil
}

func (s *Server) lookup(k objectKey) ([]byte, bool) {
	switch k.category {
	case wire.ObjectRef_BLOCK:
		if s.callbacks.BlockBytes != nil {
			return s.callbacks.BlockBytes(k.id)
		}
	case wire.ObjectRef_TX:
		if s.callbacks.TxBytes != nil {
			return s.callbacks.TxBytes(k.id)
		}
	case wire.ObjectRef_INPUT:
		if s.callbacks.InputBytes != nil {
			return s.callbacks.InputBytes(k.id)
		}
	}
	return nil, false
}

func (s *Server) has(k objectKey) bool {
	switch k.category {
	case wire.ObjectRef_BLOCK:
		if k.id == s.config.GenesisHash {
			return true
		}
		if s.callbacks.HasBlock != nil {
			return s.callbacks.HasBlock(k.id)
		}
	case wire.ObjectRef_TX:
		if s.callbacks.HasTx != nil {
			return s.callbacks.HasTx(k.id)
		}
	}
	_, ok := s.lookup(k)
	return ok
}

func (s *Server) announce(k objectKey, skip *peer) {
	f, err := frame(wire.Frame_INV, &wire.Inventory{Items: []*wire.ObjectRef{ref(k.category, k.id)}})
	if err == nil {
		s.broadcast(f, skip)
	}
}

// BroadcastTx advertises a locally admitted transaction to every peer.
func (s *Server) BroadcastTx(data []byte, skip *peer) {
	tx, err := codec.DecodeTx(data)
	if err == nil {
		s.announce(objectKey{wire.ObjectRef_TX, codec.TxID(tx)}, skip)
	}
}

// BroadcastBlock advertises a fully validated, durably stored block.
func (s *Server) BroadcastBlock(data []byte, skip *peer) {
	block, err := codec.DecodeBlock(data)
	if err == nil {
		s.announce(objectKey{wire.ObjectRef_BLOCK, codec.BlockID(block)}, skip)
	}
}

// RequestInput asks connected peers for bytes under a signed query commitment.
// Repeated calls during one timeout share a request. Unsolicited inputs are
// ignored and the digest is verified before forwarding bytes to local storage.
func (s *Server) RequestInput(commitment types.Hash) {
	k := objectKey{wire.ObjectRef_INPUT, commitment}
	if s.has(k) {
		return
	}
	s.mu.Lock()
	for id, t := range s.wantedInputs {
		if time.Since(t) > requestTimeout {
			delete(s.wantedInputs, id)
		}
	}
	if _, exists := s.wantedInputs[commitment]; exists || len(s.wantedInputs) >= maxInventory || len(s.peers) == 0 {
		s.mu.Unlock()
		return
	}
	s.wantedInputs[commitment] = time.Now()
	s.mu.Unlock()
	f, err := frame(wire.Frame_GET_DATA, ref(k.category, k.id))
	if err == nil {
		s.broadcast(f, nil)
		s.emit("input_fetch", "started", "requested committed bytes from peers", map[string]any{"commitment": commitment.String()})
	}
}

// BroadcastInput announces only a digest. Peers fetch it when a local query needs it.
func (s *Server) BroadcastInput(commitment types.Hash) {
	s.announce(objectKey{wire.ObjectRef_INPUT, commitment}, nil)
}

func (s *Server) wantsInput(id types.Hash) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.wantedInputs[id]
	return ok && time.Since(t) <= requestTimeout
}
