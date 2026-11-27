// Block assembly commits the late response list only after the first hit.
package miner

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sort"
	"time"

	"github.com/thomas/poml-node/internal/codec"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	proofutil "github.com/thomas/poml-node/internal/proof"
	"github.com/thomas/poml-node/internal/types"
)

func (m *Miner) response(height, sequence uint64, w types.Winner) types.Tx {
	started := time.Now()
	tx := types.Tx{Kind: types.TxResponse, Sender: w.MinerID, MinerID: w.MinerID, QueryID: w.QueryID, Parent: w.Parent, PrefixDigest: w.PrefixDigest,
		AttemptID: w.AttemptID, ModelID: w.ModelID, Ciphertext: w.Ciphertext, TicketHash: w.TicketHash, CreatedAt: height, CompletionSequence: sequence, Rho: w.Rho, RhoProof: w.RhoProof, CTicket: w.CTicket}
	tx.Signature = cryptoutil.Sign(m.config.Identity, "transaction", codec.TxSigningBytes(tx))
	m.record("response_create", "complete", "signed optimistic response", map[string]any{
		"tx_id": codec.TxID(tx).String(), "qid": w.QueryID, "attempt_id": w.AttemptID,
		"parent_hash": w.Parent.String(), "prefix_digest": w.PrefixDigest.String(), "ticket_hash": w.TicketHash.String(),
		"created_height": height, "completion_sequence": sequence, "bytes": len(codec.TxBytes(tx)),
		"wall_ns": time.Since(started).Nanoseconds(), "C_service": 1,
	})
	return tx
}

func (m *Miner) publish(ctx context.Context, r miningRound, winner attemptResult, losses []types.Tx) error {
	w := winner.candidate
	block := types.Block{
		Header: types.Header{Parent: r.parent, Height: r.height, Producer: m.config.MinerID, Timestamp: time.Now().UnixNano(), PrefixDigest: r.digest, TicketHash: w.TicketHash},
		Prefix: r.prefix, Winner: w,
	}
	selected, err := m.fitResponses(block, losses)
	if err != nil {
		return err
	}
	block.Responses = losses[:selected]
	block.Header.ClaimDigest = codec.ClaimDigest(block.Responses)
	fields := map[string]any{
		"qid": w.QueryID, "attempt_id": w.AttemptID, "parent_hash": r.parent.String(),
		"height": r.height, "ticket_hash": w.TicketHash.String(),
		"prefix_digest":     r.digest.String(),
		"claim_list_digest": block.Header.ClaimDigest.String(), "observed_losses": len(losses),
		"included_losses": selected, "overflow": len(losses) - selected,
	}
	m.record("response_assembly", "complete", "ordered current-round losses", fields)
	cert, modelProof, err := m.certify(ctx, "winner", w, codec.WinnerStatement(block), winner.witness)
	if err != nil {
		return err
	}
	block.Winner.Certificate = cert
	if m.config.Model.UsesProofBundle() {
		if len(modelProof) == 0 {
			// The Go fixture predates the optional model-proof response field.
			// Keep its execution token in the envelope until a real model
			// backend supplies a proof object.
			modelProof = winner.witness.Execution
		}
		bundle, bundleErr := proofutil.Encode(proofutil.Bundle{
			ProfileID: m.config.Model.CertificateProfile, SetupDigest: m.config.Model.Digest(),
			StatementHash: proofutil.StatementDigest(codec.WinnerStatement(block)),
			ModelProof:    modelProof, BindingProof: cert,
		})
		if bundleErr != nil {
			return bundleErr
		}
		block.Winner.ProofBundle = bundle
		block.Winner.Certificate = nil
	}
	block.Header.BodyDigest = codec.BodyDigest(block)
	block.Signature = cryptoutil.Sign(m.config.Identity, "block", codec.BlockUnsignedBytes(block))
	fields["block_hash"], fields["bytes"] = codec.BlockID(block).String(), len(codec.BlockBytes(block))
	m.record("block_build", "complete", "candidate signed", fields)
	if err := m.config.SubmitBlock(block); err != nil {
		return err
	}
	m.record("block_publish", "accepted", "complete local block accepted", fields)
	if m.config.OnBlock != nil {
		m.config.OnBlock(block)
	}
	for _, tx := range losses[selected:] {
		m.queue(tx, "block byte or validation limit")
	}
	return nil
}

// fitResponses reserves exact bytes for the two fixed-size Ed25519 signatures.
// Every bound increases monotonically with the number of encoded responses.
func (m *Miner) fitResponses(block types.Block, losses []types.Tx) (int, error) {
	block.Signature = make([]byte, ed25519.SignatureSize)
	block.Winner.Certificate = make([]byte, ed25519.SignatureSize)
	if m.config.Model.UsesProofBundle() {
		// The current certificate-backed bundle contains a small execution token.
		// Reserve a bounded envelope before the response suffix is selected. A
		// future model-proof profile must set a larger explicit block budget.
		block.Winner.ProofBundle = make([]byte, m.config.Model.ProofReserveBytes())
	}
	check := m.config.CheckLimits
	if check == nil {
		check = func(b types.Block) error {
			if uint64(len(b.Responses)) > m.config.Genesis.Raw.ResponseLimit || uint64(len(codec.BlockBytes(b))) > m.config.Genesis.Raw.MaxBlockBytes {
				return errors.New("block exceeds public limits")
			}
			return nil
		}
	}
	if err := check(block); err != nil {
		return 0, err
	}
	count := sort.Search(len(losses), func(i int) bool { block.Responses = losses[:i+1]; return check(block) != nil })
	return count, nil
}
