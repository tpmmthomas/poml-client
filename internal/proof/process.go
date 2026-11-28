package proof

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/thomas/poml-node/internal/types"
)

// ModelVerifier is the consensus-facing backend hook for model proofs.
// Implementations must fail closed when the proof runtime or setup artifacts
// are unavailable.
type ModelVerifier interface {
	Verify(proofProfile string, setupDigest types.Hash, model ModelProof) error
}

// ProcessVerifier invokes a pinned verifier process. Keeping the process
// boundary explicit lets the Go node use the maintained EZKL Python runtime
// without importing Python into the consensus binary.
type ProcessVerifier struct {
	Command        []string
	ModelProfileID string
	ProfilesDir    string
	CacheDir       string
	Timeout        time.Duration
}

type processRequest struct {
	ModelProfileID string `json:"model_profile_id"`
	ProofProfile   string `json:"proof_profile"`
	SetupDigest    string `json:"setup_digest"`
	PublicInstance string `json:"public_instance_base64"`
	Proof          string `json:"proof_base64"`
}

type processResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Verify runs the configured verifier with bounded input and execution time.
func (v ProcessVerifier) Verify(proofProfile string, setupDigest types.Hash, model ModelProof) error {
	if len(v.Command) == 0 {
		return errors.New("model proof verifier command is not configured")
	}
	if v.ProfilesDir == "" || v.CacheDir == "" {
		return errors.New("model proof verifier paths are not configured")
	}
	request := processRequest{
		ModelProfileID: v.ModelProfileID,
		ProofProfile:   proofProfile,
		SetupDigest:    setupDigest.String(),
		PublicInstance: base64.StdEncoding.EncodeToString(model.PublicInstance),
		Proof:          base64.StdEncoding.EncodeToString(model.Proof),
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode model proof request: %w", err)
	}
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append([]string(nil), v.Command[1:]...)
	args = append(args, "--profiles-dir", v.ProfilesDir, "--cache-dir", v.CacheDir)
	command := exec.CommandContext(ctx, v.Command[0], args...)
	command.Stdin = bytes.NewReader(payload)
	output, err := command.Output()
	if ctx.Err() != nil {
		return fmt.Errorf("model proof verifier timed out after %s", timeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := bytes.TrimSpace(exitErr.Stderr)
			if len(message) > 1024 {
				message = message[:1024]
			}
			return fmt.Errorf("model proof verifier failed: %s", message)
		}
		return fmt.Errorf("start model proof verifier: %w", err)
	}
	var response processResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return fmt.Errorf("decode model proof verifier response: %w", err)
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "verifier rejected the proof"
		}
		return errors.New(response.Error)
	}
	return nil
}
