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
