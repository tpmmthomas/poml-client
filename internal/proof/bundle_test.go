package proof

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/types"
)

func TestBundleRoundTripAndStatementBinding(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	statement := []byte("canonical winner statement")
	binding := cryptoutil.Sign(private, "certificate", statement)
	setup := types.HashBytes("setup", []byte("small-diffusion"))
	encoded, err := Encode(Bundle{
		ProfileID: "aux-ed25519-v1", SetupDigest: setup,
		StatementHash: StatementDigest(statement), ModelProof: []byte("model-proof"), BindingProof: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := VerifyBundle(encoded, "aux-ed25519-v1", setup, statement, public)
	if err != nil || string(decoded.ModelProof) != "model-proof" {
		t.Fatalf("verify bundle: %v", err)
	}
	if _, err := VerifyBundle(encoded, "aux-ed25519-v1", setup, []byte("changed"), public); err == nil {
		t.Fatal("changed statement was accepted")
	}
	if _, err := Decode(append(encoded, 0)); err == nil {
		t.Fatal("trailing bytes were accepted")
	}
}

func TestModelProofInstanceBindsCertificate(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	statement := []byte("winner")
	instance := []byte(`[ ["field-element"] ]`)
	modelProof, err := EncodeModelProof(ModelProof{Backend: "ezkl", PublicInstance: instance, Proof: []byte(`{"proof":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	setup := types.HashBytes("setup", []byte("ezkl"))
	binding := cryptoutil.Sign(private, "certificate", BindingStatement(statement, instance))
	encoded, err := Encode(Bundle{ProfileID: "zk-ezkl-diffusion-v1", SetupDigest: setup,
		StatementHash: StatementDigest(statement), ModelProof: modelProof, BindingProof: binding})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(encoded, "zk-ezkl-diffusion-v1", setup, statement, public, stubVerifier{}); err != nil {
		t.Fatal(err)
	}
	wrong := append([]byte(nil), instance...)
	wrong[3] = 'X'
	wrongBinding := cryptoutil.Sign(private, "certificate", BindingStatement(statement, wrong))
	wrongBundle, err := Encode(Bundle{ProfileID: "zk-ezkl-diffusion-v1", SetupDigest: setup,
		StatementHash: StatementDigest(statement), ModelProof: modelProof, BindingProof: wrongBinding})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(wrongBundle, "zk-ezkl-diffusion-v1", setup, statement, public, stubVerifier{}); err == nil {
		t.Fatal("certificate for a different instance was accepted")
	}
	wrongBackend, err := EncodeModelProof(ModelProof{Backend: "other", PublicInstance: instance, Proof: []byte(`{"proof":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	wrongBackendBundle, err := Encode(Bundle{ProfileID: "zk-ezkl-diffusion-v1", SetupDigest: setup,
		StatementHash: StatementDigest(statement), ModelProof: wrongBackend,
		BindingProof: cryptoutil.Sign(private, "certificate", BindingStatement(statement, instance))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(wrongBackendBundle, "zk-ezkl-diffusion-v1", setup, statement, public, stubVerifier{}); err == nil {
		t.Fatal("unexpected model proof backend was accepted")
	}
}

type stubVerifier struct{}

func (stubVerifier) Verify(string, types.Hash, ModelProof) error { return nil }
