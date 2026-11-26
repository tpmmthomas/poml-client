// Attempt execution is independent of the network view once a prefix is frozen.
package miner

import (
	"bytes"
	"context"
	"errors"
	"time"

	pb "github.com/thomas/poml-node/api/proto"
	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/consensus"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/internal/worker"
)

type attemptResult struct {
	candidate types.Winner
	witness   *pb.Witness
	hit       bool
	err       error
}

func (m *Miner) attempt(ctx context.Context, parent, prefix types.Hash, q types.Query, id, purpose string) attemptResult {
	w := types.Winner{
		Parent: parent, PrefixDigest: prefix, MinerID: m.config.MinerID,
		ModelID: m.config.Model.ID, QueryID: q.ID, AttemptID: id,
		CTicket: m.config.Model.TicketWeight,
	}
	w.ProfileDigest = m.config.Model.Digest()
	w.InputCommitment, w.Recipient = q.Commitment, q.Recipient
	requestID := purpose + "/" + id
	log := func(kind, status, reason string, fields map[string]any) {
		fields["purpose"] = purpose
		fields["parent_hash"], fields["prefix_digest"] = parent.String(), prefix.String()
		fields["qid"], fields["attempt_id"], fields["request_id"] = q.ID, id, requestID
		m.record(kind, status, reason, fields)
	}
	started := time.Now()
	log("attempt_start", "started", "dispatching attempt", map[string]any{"C_ticket": w.CTicket})
	input := codec.AttemptInput(parent, prefix, w.ModelID, q.ID, w.MinerID, id)
	rho, proof, inferenceKey, err := cryptoutil.VRFProve(m.config.InferenceSeed, input)
	if err != nil {
		return attemptResult{candidate: w, err: err}
	}
	w.Rho, w.RhoProof, w.InferenceVRFPublic = rho, proof, inferenceKey
	slots, err := cryptoutil.ExpandRandomness(m.config.Model, input, rho)
	if err != nil {
		return attemptResult{candidate: w, err: err}
	}
	request := worker.InferenceRequest{
		Spec: m.config.Model, RequestID: requestID, QueryID: q.ID, AttemptID: id, MinerID: w.MinerID,
		Parent: parent, Prefix: prefix, Commitment: q.Commitment, SignedQuery: q.SignedTx,
		Input: q.Input, Randomness: slots,
	}
	result, err := m.runInference(ctx, request, log)
	if err != nil {
		return attemptResult{candidate: w, err: err}
	}
	encStart := time.Now()
	encInput := types.HashBytes("encryption-input", input)
	coins, encProof, encryptionKey, err := cryptoutil.VRFProve(m.config.EncryptionSeed, encInput[:])
	if err != nil {
		return attemptResult{candidate: w, err: err}
	}
	w.EncryptionVRFPublic = encryptionKey
	w.Ciphertext, err = cryptoutil.SealOutput(q.Recipient, coins, m.config.Genesis.Raw.ChainID, w, result.Output)
	if err != nil {
		return attemptResult{candidate: w, err: err}
	}
	log("encryption", "complete", "canonical ciphertext ready", map[string]any{
		"wall_ns": time.Since(encStart).Nanoseconds(), "ciphertext_bytes": len(w.Ciphertext),
	})
	w.TicketHash = types.HashBytes("ticket", codec.TicketBytes(w))
	threshold, err := consensus.Threshold(m.config.Genesis.Raw.HashBits, m.config.Genesis.BaseTarget, w.CTicket)
	if err != nil {
		return attemptResult{candidate: w, err: err}
	}
	wit := &pb.Witness{
		Execution: result.Witness, Output: result.Output, EncryptionVrf: coins,
		EncryptionProof: encProof, EncryptionInput: encInput[:], Recipient: q.Recipient,
		Randomness: slots, Input: q.Input, SignedQuery: q.SignedTx, InferenceVrf: rho,
		InferenceProof: proof, InferencePublicKey: inferenceKey,
		EncryptionPublicKey: encryptionKey, AttemptInput: input,
		ChainId: m.config.Genesis.Raw.ChainID,
	}
	log("attempt_finish", "complete", "ticket checked", map[string]any{
		"ticket_hash": w.TicketHash.String(), "wall_ns": time.Since(started).Nanoseconds(),
	})
	return attemptResult{candidate: w, witness: wit, hit: consensus.TicketWins(w.TicketHash, threshold)}
}

func (m *Miner) runInference(ctx context.Context, request worker.InferenceRequest,
	log func(string, string, string, map[string]any)) (worker.InferenceResult, error) {
	log("worker_start", "started", "inference requested", map[string]any{
		"input_bytes": len(request.Input), "randomness_slots": len(request.Randomness),
	})
	workStart := time.Now()
	result, err := m.config.Worker.Infer(ctx, request)
	if err != nil {
		kind := "worker_failure"
		if ctx.Err() != nil {
			kind = "worker_cancel"
		}
		log(kind, "failed", err.Error(), map[string]any{"wall_ns": time.Since(workStart).Nanoseconds()})
		return worker.InferenceResult{}, err
	}
	inferFields := map[string]any{
		"inference_ns": result.Duration.Nanoseconds(),
		"wall_ns":      time.Since(workStart).Nanoseconds(), "output_bytes": len(result.Output),
		"operation_count": result.OperationCount,
	}
	if result.CPUNanos != nil {
		inferFields["cpu_ns"] = *result.CPUNanos
	}
	log("worker_finish", "complete", "inference returned", inferFields)
	return result, nil
}

// CertifyChallenge replays the original deterministic worker request and encryption.
// A pledge on another branch or attempt cannot be answered with a fresh execution context.
func (m *Miner) CertifyChallenge(ctx context.Context, q types.Query, response types.Tx, statement []byte) ([]byte, error) {
	if q.ID != response.QueryID || m.config.MinerID != response.MinerID {
		return nil, errors.New("challenge targets a different query or miner")
	}
	result := m.attempt(ctx, response.Parent, response.PrefixDigest, q, response.AttemptID, "challenge")
	if result.err != nil {
		return nil, result.err
	}
	w := result.candidate
	if w.TicketHash != response.TicketHash || !bytes.Equal(w.Ciphertext, response.Ciphertext) {
		return nil, errors.New("replayed result differs from claimed response")
	}
	return m.certify(ctx, "challenge", w, statement, result.witness)
}

func (m *Miner) certify(ctx context.Context, purpose string, w types.Winner, statement []byte, witness *pb.Witness) ([]byte, error) {
	fields := map[string]any{
		"attempt_id": w.AttemptID, "qid": w.QueryID, "parent_hash": w.Parent.String(),
		"request_id": purpose + "/" + w.AttemptID, "purpose": purpose,
		"ticket_hash": w.TicketHash.String(),
	}
	m.record("proof_queue", "queued", "certificate request assembled", fields)
	m.record("proof_start", "started", "configured proof worker requested", fields)
	started := time.Now()
	cert, timing, err := m.config.Worker.Certify(ctx, m.config.Model, purpose+"/"+w.AttemptID, purpose, statement, witness)
	if err == nil && !m.config.Model.AcceptsRandomProof() {
		err = cryptoutil.Verify(m.config.Genesis.CertificatePublic, cert, "certificate", statement)
	}
	fields["wall_ns"], fields["certification_ns"], fields["C_cert"] = time.Since(started).Nanoseconds(), timing.Duration.Nanoseconds(), 1
	if timing.CPUNanos != nil {
		fields["cpu_ns"] = *timing.CPUNanos
	}
	if err != nil {
		m.record("proof_failure", "failed", err.Error(), fields)
		return nil, err
	}
	reason := "certificate verified"
	if m.config.Model.AcceptsRandomProof() {
		reason = "dummy proof accepted by configured policy"
	}
	m.record("proof_finish", "complete", reason, fields)
	return cert, nil
}
