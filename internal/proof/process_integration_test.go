package proof

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thomas/poml-node/internal/types"
)

// TestProcessVerifierEZKLIntegration is opt-in because the proof artifacts
// are large and are intentionally kept outside the repository.
func TestProcessVerifierEZKLIntegration(t *testing.T) {
	proofPath := os.Getenv("POML_EZKL_TEST_PROOF")
	cacheDir := os.Getenv("POML_EZKL_TEST_CACHE")
	if proofPath == "" || cacheDir == "" {
		t.Skip("set POML_EZKL_TEST_PROOF and POML_EZKL_TEST_CACHE for the EZKL integration test")
	}
	proofBytes, err := os.ReadFile(proofPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Instances [][]string `json:"instances"`
	}
	if err := json.Unmarshal(proofBytes, &decoded); err != nil {
		t.Fatal(err)
	}
	profileBytes, err := os.ReadFile(filepath.Join("..", "..", "profiles", "diffusion-small-ezkl.json"))
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		ProofSetupDigest string `json:"proof_setup_digest"`
	}
	if err := json.Unmarshal(profileBytes, &profile); err != nil {
		t.Fatal(err)
	}
	setupDigest, err := types.ParseHash(profile.ProofSetupDigest)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := json.Marshal(decoded.Instances)
	if err != nil {
		t.Fatal(err)
	}
	command := os.Getenv("POML_EZKL_TEST_PYTHON")
	if command == "" {
		command = "python3"
	}
	verifier := ProcessVerifier{
		Command:        []string{command, filepath.Join("..", "..", "stage2", "proof", "verify_ezkl.py")},
		ModelProfileID: "diffusion-small-ezkl", ProfilesDir: filepath.Join("..", "..", "profiles"), CacheDir: cacheDir,
	}
	model := ModelProof{Backend: "ezkl", PublicInstance: instance, Proof: proofBytes}
	if err := verifier.Verify("zk-ezkl-diffusion-v1", setupDigest, model); err != nil {
		t.Fatal(err)
	}
}
