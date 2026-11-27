package worker

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"testing"

	pb "github.com/thomas/poml-node/api/proto"
	"github.com/thomas/poml-node/internal/codec"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/profiles"
)

func TestAuxiliaryWitnessRejectsChangedEncryptionProof(t *testing.T) {
	spec, err := profiles.Load("fixture-a-aux")
	if err != nil {
		t.Fatal(err)
	}
	identity := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	recipient, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("auxiliary witness input")
	query := types.Tx{Kind: types.TxQuery, Sender: "user", QueryID: "q-aux", Input: input,
		Commitment: types.HashBytes("input", input), Recipient: recipient.PublicKey().Bytes(), ModelID: spec.ID}
	query.Signature = cryptoutil.Sign(identity, "transaction", codec.TxSigningBytes(query))
	parent := types.HashBytes("parent", []byte("aux"))
	prefix := codec.PrefixDigest(parent, []types.Tx{query})
	attempt := codec.AttemptInput(parent, prefix, spec.ID, query.QueryID, "miner", "attempt-1")
	rho, rhoProof, inferencePublic, err := cryptoutil.VRFProve(bytes.Repeat([]byte{1}, 32), attempt)
	if err != nil {
		t.Fatal(err)
	}
	randomness, err := cryptoutil.ExpandRandomness(spec, attempt, rho)
	if err != nil {
		t.Fatal(err)
	}
	inferenceRequest := &pb.WorkerRequest{
		ProfileId: spec.ID, ModelDigest: mustHashBytes(spec.ModelDigest, t), QueryId: query.QueryID,
		AttemptId: "attempt-1", MinerId: "miner", Input: input, InputCommitment: query.Commitment[:],
		SignedQuery: codec.TxBytes(query), Parent: parent[:], PrefixDigest: prefix[:], Randomness: randomness,
	}
	inferenceReply := &pb.WorkerResponse{}
	if err := fixtureInference(spec, inferenceRequest, inferenceReply); err != nil {
		t.Fatal(err)
	}
	encryptionInput := types.HashBytes("encryption-input", attempt)
	coins, encryptionProof, encryptionPublic, err := cryptoutil.VRFProve(bytes.Repeat([]byte{2}, 32), encryptionInput[:])
	if err != nil {
		t.Fatal(err)
	}
	winner := types.Winner{
		QueryID: query.QueryID, AttemptID: "attempt-1", MinerID: "miner", ModelID: spec.ID,
		ChainID: "aux-test-chain", ProfileDigest: spec.Digest(), ProofProfile: spec.CertificateProfile,
		ProofSetupDigest: spec.Digest(), InputCommitment: query.Commitment, Recipient: query.Recipient,
		InferenceVRFPublic: inferencePublic, EncryptionVRFPublic: encryptionPublic,
		EncryptionVRFInput: encryptionInput[:], Parent: parent, PrefixDigest: prefix, Rho: rho, RhoProof: rhoProof,
		CTicket: 1,
	}
	nonce, err := cryptoutil.CommitmentNonce(winner.ChainID, winner, coins)
	if err != nil {
		t.Fatal(err)
	}
	winner.OutputCommitment = cryptoutil.OutputCommitment(nonce, inferenceReply.Output)
	winner.Ciphertext, err = cryptoutil.SealOutputBound(query.Recipient, coins, winner.ChainID, winner, nonce, inferenceReply.Output)
	if err != nil {
		t.Fatal(err)
	}
	statement := types.Block{Header: types.Header{Parent: parent, Height: 1, Producer: "miner", PrefixDigest: prefix}, Winner: winner}
	digest := spec.Digest()
	request := &pb.WorkerRequest{
		ProfileId: spec.ID, ProfileDigest: digest[:], CertificateProfile: spec.CertificateProfile,
		Purpose: "winner", Statement: codec.WinnerStatement(statement), Witness: &pb.Witness{
			Execution: inferenceReply.ExecutionWitness, Output: inferenceReply.Output,
			EncryptionVrf: coins, EncryptionProof: encryptionProof, EncryptionInput: encryptionInput[:], Recipient: query.Recipient,
			Randomness: randomness, Input: input, SignedQuery: codec.TxBytes(query), InferenceVrf: rho,
			InferenceProof: rhoProof, InferencePublicKey: inferencePublic, EncryptionPublicKey: encryptionPublic,
			AttemptInput: attempt, ChainId: winner.ChainID, OutputCommitmentNonce: nonce,
		},
	}
	if err := ValidateAuxiliaryWitness(request); err != nil {
		t.Fatal(err)
	}
	request.Witness.EncryptionProof[0] ^= 1
	if err := ValidateAuxiliaryWitness(request); err == nil {
		t.Fatal("changed encryption proof was accepted")
	}
}

func mustHashBytes(value string, t *testing.T) []byte {
	t.Helper()
	hash, err := types.ParseHash(value)
	if err != nil {
		t.Fatal(err)
	}
	return hash[:]
}
