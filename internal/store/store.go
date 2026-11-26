// Package store provides the durable objects needed to restart a node.
package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble"
	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/types"
)

type Store struct{ db *pebble.DB }

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Clean(dataDir), 0o755); err != nil {
		return nil, err
	}
	db, err := pebble.Open(filepath.Join(dataDir, "pebble"), &pebble.Options{})
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func key(prefix string, hash types.Hash) []byte {
	return []byte(fmt.Sprintf("%s/%s", prefix, hash.String()))
}

func (s *Store) PutBlock(block types.Block, state types.State) error {
	blockID := codec.BlockID(block)
	blockBytes := codec.BlockBytes(block)
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(key("block", blockID), blockBytes, nil); err != nil {
		return err
	}
	if err := batch.Set(key("state", blockID), stateBytes, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

func (s *Store) SetTip(tip types.Hash) error {
	return s.db.Set([]byte("meta/tip"), tip[:], pebble.Sync)
}

func (s *Store) PutGenesis(genesis types.Hash, state types.State) error {
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte("meta/genesis"), genesis[:], nil); err != nil {
		return err
	}
	if err := batch.Set([]byte("meta/tip"), genesis[:], nil); err != nil {
		return err
	}
	if err := batch.Set(key("state", genesis), stateBytes, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

// Genesis returns the chain identity recorded beside the durable state.
func (s *Store) Genesis() (types.Hash, error) {
	data, closer, err := s.db.Get([]byte("meta/genesis"))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return types.Hash{}, os.ErrNotExist
		}
		return types.Hash{}, err
	}
	defer closer.Close()
	if len(data) != types.HashSize {
		return types.Hash{}, errors.New("stored genesis has invalid length")
	}
	var genesis types.Hash
	copy(genesis[:], data)
	return genesis, nil
}

func (s *Store) Block(id types.Hash) (types.Block, error) {
	data, closer, err := s.db.Get(key("block", id))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return types.Block{}, os.ErrNotExist
		}
		return types.Block{}, err
	}
	defer closer.Close()
	return codec.DecodeBlock(append([]byte(nil), data...))
}

func (s *Store) State(id types.Hash) (types.State, error) {
	data, closer, err := s.db.Get(key("state", id))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return types.State{}, os.ErrNotExist
		}
		return types.State{}, err
	}
	defer closer.Close()
	var state types.State
	if err := json.Unmarshal(data, &state); err != nil {
		return types.State{}, err
	}
	if state.Accounts == nil || state.Miners == nil || state.Queries == nil || state.Claims == nil {
		return types.State{}, errors.New("stored state is incomplete")
	}
	return state, nil
}

func (s *Store) Tip() (types.Hash, error) {
	data, closer, err := s.db.Get([]byte("meta/tip"))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return types.Hash{}, os.ErrNotExist
		}
		return types.Hash{}, err
	}
	defer closer.Close()
	if len(data) != types.HashSize {
		return types.Hash{}, errors.New("stored tip has invalid length")
	}
	var tip types.Hash
	copy(tip[:], data)
	return tip, nil
}

func (s *Store) HasBlock(id types.Hash) bool {
	_, closer, err := s.db.Get(key("block", id))
	if err != nil {
		return false
	}
	closer.Close()
	return true
}

func (s *Store) PutMempool(tx types.Tx) error {
	return s.db.Set(key("mempool", codec.TxID(tx)), codec.TxBytes(tx), pebble.Sync)
}

func (s *Store) DeleteMempool(tx types.Tx) error {
	return s.db.Delete(key("mempool", codec.TxID(tx)), pebble.Sync)
}

// PutInput keeps query bytes available for input retrieval after the query
// transaction leaves the mempool.
func (s *Store) PutInput(commitment types.Hash, input []byte) error {
	if types.HashBytes("input", input) != commitment {
		return errors.New("input commitment mismatch")
	}
	return s.db.Set(key("input", commitment), append([]byte(nil), input...), pebble.Sync)
}

// Input returns bytes stored under a validated query commitment.
func (s *Store) Input(commitment types.Hash) ([]byte, error) {
	data, closer, err := s.db.Get(key("input", commitment))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), data...), nil
}

func (s *Store) Mempool() ([]types.Tx, error) {
	iterator, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte("mempool/"), UpperBound: []byte("mempool0")})
	if err != nil {
		return nil, err
	}
	defer iterator.Close()
	var transactions []types.Tx
	for iterator.First(); iterator.Valid(); iterator.Next() {
		tx, decodeErr := codec.DecodeTx(append([]byte(nil), iterator.Value()...))
		if decodeErr != nil {
			return nil, decodeErr
		}
		transactions = append(transactions, tx)
	}
	return transactions, iterator.Error()
}

func (s *Store) NextAttempt(minerID string) (uint64, error) {
	keyBytes := []byte("meta/attempt/" + minerID)
	current := uint64(0)
	if data, closer, err := s.db.Get(keyBytes); err == nil {
		if len(data) == 8 {
			current = binary.BigEndian.Uint64(data)
		}
		closer.Close()
	} else if !errors.Is(err, pebble.ErrNotFound) {
		return 0, err
	}
	current++
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], current)
	if err := s.db.Set(keyBytes, encoded[:], pebble.Sync); err != nil {
		return 0, err
	}
	return current, nil
}
