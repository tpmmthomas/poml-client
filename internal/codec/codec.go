// Package codec defines the one byte representation accepted by consensus.
// Network messages wrap these bytes in protobuf, but hashes and signatures
// always use this explicit length-prefixed format.
package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/thomas/poml-node/internal/types"
)

const (
	version  = byte(1)
	maxField = 16 << 20
	maxItems = 4096
)

type writer struct{ bytes.Buffer }

func (w *writer) byte(value byte) { _ = w.WriteByte(value) }

func (w *writer) u64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = w.Write(encoded[:])
}

func (w *writer) i64(value int64) { w.u64(uint64(value)) }

func (w *writer) bytes(value []byte) {
	w.u64(uint64(len(value)))
	_, _ = w.Write(value)
}

func (w *writer) string(value string) { w.bytes([]byte(value)) }

func (w *writer) hash(value types.Hash) { _, _ = w.Write(value[:]) }

func (w *writer) tx(tx types.Tx, signature bool) {
	w.byte(byte(tx.Kind))
	w.string(tx.Sender)
	w.u64(tx.Nonce)
	w.string(tx.QueryID)
	w.bytes(tx.Input)
	w.hash(tx.Commitment)
	w.bytes(tx.Recipient)
	w.string(tx.ModelID)
	w.u64(tx.Expiry)
	w.u64(tx.Amount)
	w.u64(tx.Fee)
	w.hash(tx.Parent)
	w.hash(tx.PrefixDigest)
	w.string(tx.AttemptID)
	w.string(tx.MinerID)
	w.u64(tx.CreatedAt)
	w.u64(tx.CompletionSequence)
	w.bytes(tx.Rho)
	w.bytes(tx.RhoProof)
	w.u64(tx.CTicket)
	w.bytes(tx.Ciphertext)
	w.hash(tx.TicketHash)
	w.bytes(tx.ProofStatement)
	w.bytes(tx.Proof)
	w.bytes(tx.IdentityPublic)
	w.bytes(tx.InferenceVRFPublic)
	w.bytes(tx.EncryptionVRFPublic)
	if signature {
		w.bytes(tx.Signature)
	}
}

func (w *writer) winner(winner types.Winner) {
	w.string(winner.QueryID)
	w.string(winner.AttemptID)
	w.string(winner.MinerID)
	w.string(winner.ModelID)
	w.string(winner.ChainID)
	w.hash(winner.ProfileDigest)
	w.string(winner.ProofProfile)
	w.hash(winner.ProofSetupDigest)
	w.hash(winner.InputCommitment)
	w.bytes(winner.Recipient)
	w.bytes(winner.InferenceVRFPublic)
	w.bytes(winner.EncryptionVRFPublic)
	w.bytes(winner.EncryptionVRFInput)
	w.hash(winner.Parent)
	w.hash(winner.PrefixDigest)
	w.bytes(winner.Rho)
	w.bytes(winner.RhoProof)
	w.bytes(winner.Ciphertext)
	w.hash(winner.OutputCommitment)
	w.u64(winner.CTicket)
	w.hash(winner.TicketHash)
	w.bytes(winner.ProofBundle)
	w.bytes(winner.Certificate)
}

func (w *writer) block(block types.Block, signature bool) {
	w.byte(version)
	w.hash(block.Header.Parent)
	w.u64(block.Header.Height)
	w.string(block.Header.Producer)
	w.i64(block.Header.Timestamp)
	w.hash(block.Header.PrefixDigest)
	w.hash(block.Header.ClaimDigest)
	w.hash(block.Header.BodyDigest)
	w.hash(block.Header.TicketHash)
	w.u64(uint64(len(block.Prefix)))
	for _, tx := range block.Prefix {
		w.tx(tx, true)
	}
	w.u64(uint64(len(block.Responses)))
	for _, tx := range block.Responses {
		w.tx(tx, true)
	}
	w.winner(block.Winner)
	if signature {
		w.bytes(block.Signature)
	}
}

func TxSigningBytes(tx types.Tx) []byte {
	var w writer
	w.byte(version)
	w.tx(tx, false)
	return w.Bytes()
}

func TxBytes(tx types.Tx) []byte {
	var w writer
	w.byte(version)
	w.tx(tx, true)
	return w.Bytes()
}

func TxID(tx types.Tx) types.Hash { return types.HashBytes("tx", TxSigningBytes(tx)) }

func PrefixDigest(parent types.Hash, prefix []types.Tx) types.Hash {
	var w writer
	w.hash(parent)
	w.u64(uint64(len(prefix)))
	for _, tx := range prefix {
		w.hash(TxID(tx))
	}
	return types.HashBytes("prefix", w.Bytes())
}

func ClaimDigest(responses []types.Tx) types.Hash {
	var w writer
	w.u64(uint64(len(responses)))
	for _, tx := range responses {
		w.hash(TxID(tx))
	}
	return types.HashBytes("claims", w.Bytes())
}

func BodyDigest(block types.Block) types.Hash {
	var w writer
	w.u64(uint64(len(block.Prefix)))
	for _, tx := range block.Prefix {
		w.bytes(TxBytes(tx))
	}
	w.u64(uint64(len(block.Responses)))
	for _, tx := range block.Responses {
		w.bytes(TxBytes(tx))
	}
	w.winner(block.Winner)
	return types.HashBytes("body", w.Bytes())
}

func AttemptInput(parent, prefix types.Hash, modelID, queryID, minerID, attemptID string) []byte {
	var w writer
	w.byte(version)
	w.hash(parent)
	w.hash(prefix)
	w.string(modelID)
	w.string(queryID)
	w.string(minerID)
	w.string(attemptID)
	return append([]byte("poml-stage1/inference/"), w.Bytes()...)
}

func TicketBytes(winner types.Winner) []byte {
	var w writer
	w.byte(version)
	w.hash(winner.Parent)
	w.hash(winner.PrefixDigest)
	w.string(winner.ModelID)
	w.string(winner.QueryID)
	w.string(winner.MinerID)
	w.string(winner.AttemptID)
	w.bytes(winner.Rho)
	w.u64(winner.CTicket)
	w.bytes(winner.Ciphertext)
	return w.Bytes()
}

func WinnerStatement(block types.Block) []byte {
	var w writer
	w.hash(block.Header.Parent)
	w.u64(block.Header.Height)
	w.string(block.Header.Producer)
	w.hash(block.Header.PrefixDigest)
	w.hash(block.Header.ClaimDigest)
	winner := block.Winner
	// Proof bytes and the legacy certificate sign the statement and therefore
	// cannot sign themselves. The envelope is checked against this same digest.
	winner.ProofBundle = nil
	winner.Certificate = nil
	w.winner(winner)
	return w.Bytes()
}

// DecodeWinnerStatement parses the public statement supplied to a prover.
// The returned block contains only the header fields that participate in the
// statement and a winner with proof bytes cleared.
func DecodeWinnerStatement(data []byte) (types.Block, error) {
	r := &reader{bytes.NewReader(data)}
	var block types.Block
	var err error
	if block.Header.Parent, err = r.hash(); err != nil {
		return block, err
	}
	if block.Header.Height, err = r.u64(); err != nil {
		return block, err
	}
	if block.Header.Producer, err = r.string(); err != nil {
		return block, err
	}
	if block.Header.PrefixDigest, err = r.hash(); err != nil {
		return block, err
	}
	if block.Header.ClaimDigest, err = r.hash(); err != nil {
		return block, err
	}
	if block.Winner, err = r.winner(); err != nil {
		return block, err
	}
	if r.Len() != 0 || len(block.Winner.ProofBundle) != 0 || len(block.Winner.Certificate) != 0 {
		return types.Block{}, errors.New("invalid winner statement")
	}
	return block, nil
}

func BlockUnsignedBytes(block types.Block) []byte {
	var w writer
	w.block(block, false)
	return w.Bytes()
}

func BlockBytes(block types.Block) []byte {
	var w writer
	w.block(block, true)
	return w.Bytes()
}

func BlockID(block types.Block) types.Hash {
	return types.HashBytes("block", BlockUnsignedBytes(block))
}

type reader struct{ *bytes.Reader }

func (r *reader) byte() (byte, error) { return r.ReadByte() }

func (r *reader) u64() (uint64, error) {
	var encoded [8]byte
	if _, err := io.ReadFull(r, encoded[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(encoded[:]), nil
}

func (r *reader) bytes() ([]byte, error) {
	length, err := r.u64()
	if err != nil {
		return nil, err
	}
	if length > maxField || length > uint64(r.Len()) {
		return nil, errors.New("field is too large")
	}
	value := make([]byte, length)
	if _, err := io.ReadFull(r, value); err != nil {
		return nil, err
	}
	return value, nil
}

func (r *reader) string() (string, error) {
	value, err := r.bytes()
	return string(value), err
}

func (r *reader) hash() (types.Hash, error) {
	var value types.Hash
	if _, err := io.ReadFull(r, value[:]); err != nil {
		return value, err
	}
	return value, nil
}

func (r *reader) tx() (types.Tx, error) {
	var tx types.Tx
	kind, err := r.byte()
	if err != nil {
		return tx, err
	}
	tx.Kind = types.TxKind(kind)
	if tx.Sender, err = r.string(); err != nil {
		return tx, err
	}
	if tx.Nonce, err = r.u64(); err != nil {
		return tx, err
	}
	if tx.QueryID, err = r.string(); err != nil {
		return tx, err
	}
	if tx.Input, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.Commitment, err = r.hash(); err != nil {
		return tx, err
	}
	if tx.Recipient, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.ModelID, err = r.string(); err != nil {
		return tx, err
	}
	if tx.Expiry, err = r.u64(); err != nil {
		return tx, err
	}
	if tx.Amount, err = r.u64(); err != nil {
		return tx, err
	}
	if tx.Fee, err = r.u64(); err != nil {
		return tx, err
	}
	if tx.Parent, err = r.hash(); err != nil {
		return tx, err
	}
	if tx.PrefixDigest, err = r.hash(); err != nil {
		return tx, err
	}
	if tx.AttemptID, err = r.string(); err != nil {
		return tx, err
	}
	if tx.MinerID, err = r.string(); err != nil {
		return tx, err
	}
	if tx.CreatedAt, err = r.u64(); err != nil {
		return tx, err
	}
	if tx.CompletionSequence, err = r.u64(); err != nil {
		return tx, err
	}
	if tx.Rho, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.RhoProof, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.CTicket, err = r.u64(); err != nil {
		return tx, err
	}
	if tx.Ciphertext, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.TicketHash, err = r.hash(); err != nil {
		return tx, err
	}
	if tx.ProofStatement, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.Proof, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.IdentityPublic, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.InferenceVRFPublic, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.EncryptionVRFPublic, err = r.bytes(); err != nil {
		return tx, err
	}
	if tx.Signature, err = r.bytes(); err != nil {
		return tx, err
	}
	return tx, nil
}

func (r *reader) winner() (types.Winner, error) {
	var winner types.Winner
	var err error
	if winner.QueryID, err = r.string(); err != nil {
		return winner, err
	}
	if winner.AttemptID, err = r.string(); err != nil {
		return winner, err
	}
	if winner.MinerID, err = r.string(); err != nil {
		return winner, err
	}
	if winner.ModelID, err = r.string(); err != nil {
		return winner, err
	}
	if winner.ChainID, err = r.string(); err != nil {
		return winner, err
	}
	if winner.ProfileDigest, err = r.hash(); err != nil {
		return winner, err
	}
	if winner.ProofProfile, err = r.string(); err != nil {
		return winner, err
	}
	if winner.ProofSetupDigest, err = r.hash(); err != nil {
		return winner, err
	}
	if winner.InputCommitment, err = r.hash(); err != nil {
		return winner, err
	}
	if winner.Recipient, err = r.bytes(); err != nil {
		return winner, err
	}
	if winner.InferenceVRFPublic, err = r.bytes(); err != nil {
		return winner, err
	}
	if winner.EncryptionVRFPublic, err = r.bytes(); err != nil {
		return winner, err
	}
	if winner.EncryptionVRFInput, err = r.bytes(); err != nil {
		return winner, err
	}
	if winner.Parent, err = r.hash(); err != nil {
		return winner, err
	}
	if winner.PrefixDigest, err = r.hash(); err != nil {
		return winner, err
	}
	if winner.Rho, err = r.bytes(); err != nil {
		return winner, err
	}
	if winner.RhoProof, err = r.bytes(); err != nil {
		return winner, err
	}
	if winner.Ciphertext, err = r.bytes(); err != nil {
		return winner, err
	}
	if winner.OutputCommitment, err = r.hash(); err != nil {
		return winner, err
	}
	if winner.CTicket, err = r.u64(); err != nil {
		return winner, err
	}
	if winner.TicketHash, err = r.hash(); err != nil {
		return winner, err
	}
	if winner.ProofBundle, err = r.bytes(); err != nil {
		return winner, err
	}
	if winner.Certificate, err = r.bytes(); err != nil {
		return winner, err
	}
	return winner, nil
}

func DecodeTx(data []byte) (types.Tx, error) {
	r := &reader{bytes.NewReader(data)}
	versionByte, err := r.byte()
	if err != nil || versionByte != version {
		return types.Tx{}, errors.New("unsupported transaction codec version")
	}
	tx, err := r.tx()
	if err != nil || r.Len() != 0 {
		return types.Tx{}, fmt.Errorf("invalid transaction encoding: %w", err)
	}
	return tx, nil
}

func DecodeBlock(data []byte) (types.Block, error) {
	r := &reader{bytes.NewReader(data)}
	versionByte, err := r.byte()
	if err != nil || versionByte != version {
		return types.Block{}, errors.New("unsupported block codec version")
	}
	var block types.Block
	if block.Header.Parent, err = r.hash(); err != nil {
		return block, err
	}
	if block.Header.Height, err = r.u64(); err != nil {
		return block, err
	}
	if block.Header.Producer, err = r.string(); err != nil {
		return block, err
	}
	var timestamp uint64
	if timestamp, err = r.u64(); err != nil {
		return block, err
	}
	block.Header.Timestamp = int64(timestamp)
	if block.Header.PrefixDigest, err = r.hash(); err != nil {
		return block, err
	}
	if block.Header.ClaimDigest, err = r.hash(); err != nil {
		return block, err
	}
	if block.Header.BodyDigest, err = r.hash(); err != nil {
		return block, err
	}
	if block.Header.TicketHash, err = r.hash(); err != nil {
		return block, err
	}
	var count uint64
	if count, err = r.u64(); err != nil || count > maxItems {
		return block, errors.New("invalid prefix count")
	}
	block.Prefix = make([]types.Tx, 0, count)
	for i := uint64(0); i < count; i++ {
		tx, txErr := r.tx()
		if txErr != nil {
			return block, txErr
		}
		block.Prefix = append(block.Prefix, tx)
	}
	if count, err = r.u64(); err != nil || count > maxItems {
		return block, errors.New("invalid response count")
	}
	block.Responses = make([]types.Tx, 0, count)
	for i := uint64(0); i < count; i++ {
		tx, txErr := r.tx()
		if txErr != nil {
			return block, txErr
		}
		block.Responses = append(block.Responses, tx)
	}
	if block.Winner, err = r.winner(); err != nil {
		return block, err
	}
	if block.Signature, err = r.bytes(); err != nil {
		return block, err
	}
	if r.Len() != 0 {
		return block, errors.New("trailing block bytes")
	}
	return block, nil
}
