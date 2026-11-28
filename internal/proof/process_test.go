package proof

import (
	"testing"
	"time"

	"github.com/thomas/poml-node/internal/types"
)

func TestProcessVerifierUsesBoundedProcessBoundary(t *testing.T) {
	verifier := ProcessVerifier{
		Command:        []string{"/bin/sh", "-c", "cat >/dev/null; printf '{\"ok\":true}'"},
		ModelProfileID: "diffusion-small-ezkl", ProfilesDir: "/profiles", CacheDir: "/cache",
		Timeout: time.Second,
	}
	model := ModelProof{Backend: "ezkl", PublicInstance: []byte("instance"), Proof: []byte("proof")}
	if err := verifier.Verify("zk-ezkl-diffusion-v1", types.HashBytes("setup"), model); err != nil {
		t.Fatal(err)
	}
}
