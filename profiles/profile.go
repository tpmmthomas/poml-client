// Package profiles defines the model-independent worker contract pinned in genesis.
package profiles

import (
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/thomas/poml-node/internal/types"
)

// Spec fixes resource bounds, the ordered randomness transcript and the public
// ticket weight. A node needs these fields to dispatch work and validate blocks.
type Spec struct {
	ID                 string `json:"id"`
	Backend            string `json:"backend,omitempty"`
	ModelRef           string `json:"model_ref,omitempty"`
	ModelDigest        string `json:"model_digest"`
	RandomnessSlots    uint32 `json:"randomness_slots"`
	RandomnessBytes    uint32 `json:"randomness_bytes"`
	OutputBytes        uint32 `json:"output_bytes"`
	Steps              uint32 `json:"steps,omitempty"`
	MaxTokens          uint32 `json:"max_tokens,omitempty"`
	TicketWeight       uint64 `json:"ticket_weight"`
	MaxRuntimeMS       uint32 `json:"max_runtime_ms"`
	CertificateProfile string `json:"certificate_profile"`
	ProofSetupDigest   string `json:"proof_setup_digest,omitempty"`
}

// Validate bounds computation before the scheduler or verifier accepts the profile.
func (s Spec) Validate() error {
	if s.ID == "" || s.CertificateProfile == "" {
		return errors.New("model and certificate profile IDs are required")
	}
	if strings.HasPrefix(s.CertificateProfile, "zk-") {
		if _, err := types.ParseHash(s.ProofSetupDigest); err != nil {
			return fmt.Errorf("proof setup digest: %w", err)
		}
	}
	if _, err := types.ParseHash(s.ModelDigest); err != nil {
		return fmt.Errorf("model digest: %w", err)
	}
	if s.BackendName() == "" {
		return errors.New("model backend is required")
	}
	if s.ModelRef == "" {
		s.ModelRef = s.ID
	}
	if s.RandomnessSlots < 1 || s.RandomnessSlots > 256 || s.RandomnessBytes < 1 || s.RandomnessBytes > 255*32 {
		return errors.New("invalid randomness slot dimensions")
	}
	if s.OutputBytes < 1 || s.OutputBytes > 4<<20 || s.TicketWeight < 1 || s.TicketWeight > 4096 || s.MaxRuntimeMS < 1 || s.MaxRuntimeMS > 3600000 {
		return errors.New("invalid output, ticket weight or runtime bound")
	}
	if s.Steps > 256 || s.MaxTokens > 4096 {
		return errors.New("model step or token bound is too large")
	}
	return nil
}

// Digest binds the worker handshake to the complete profile, independent of JSON layout.
func (s Spec) Digest() types.Hash {
	var numbers []byte
	for _, n := range []uint64{uint64(s.RandomnessSlots), uint64(s.RandomnessBytes), uint64(s.OutputBytes), uint64(s.Steps), uint64(s.MaxTokens), s.TicketWeight, uint64(s.MaxRuntimeMS)} {
		numbers = binary.BigEndian.AppendUint64(numbers, n)
	}
	modelRef := s.ModelRef
	if modelRef == "" {
		modelRef = s.ID
	}
	parts := [][]byte{[]byte(s.ID), []byte(s.BackendName()), []byte(modelRef), []byte(s.ModelDigest), []byte(s.CertificateProfile)}
	if s.ProofSetupDigest != "" {
		parts = append(parts, []byte(s.ProofSetupDigest))
	}
	parts = append(parts, numbers)
	return types.HashBytes("worker-profile", parts...)
}

// SetupDigest is the consensus digest for the proof verifier artifacts. A
// legacy auxiliary profile uses its worker profile digest because it has no
// external circuit setup.
func (s Spec) SetupDigest() types.Hash {
	if s.ProofSetupDigest != "" {
		digest, _ := types.ParseHash(s.ProofSetupDigest)
		return digest
	}
	return s.Digest()
}

// BackendName is the executable worker selected by this profile. Empty values
// retain the fixture behavior used by older hand-written test profiles.
func (s Spec) BackendName() string {
	if s.Backend != "" {
		return s.Backend
	}
	if strings.HasPrefix(s.ID, "fixture-") {
		return "fixture"
	}
	return "dummy"
}

// AcceptsRandomProof marks the explicitly insecure proof profile used while
// the real model proof is being developed. It is consensus-visible in the
// profile digest and must never be enabled for a production experiment.
func (s Spec) AcceptsRandomProof() bool {
	return s.CertificateProfile == "dummy-random-v1"
}

// UsesProofBundle selects the versioned Stage 2 envelope. Legacy fixture
// certificates keep their original wire shape so old Stage 1 vectors remain
// readable. New model profiles should use an explicit aux- or zk- prefix.
func (s Spec) UsesProofBundle() bool {
	return strings.HasPrefix(s.CertificateProfile, "aux-") || strings.HasPrefix(s.CertificateProfile, "zk-")
}

// ProofReserveBytes keeps suffix selection conservative before a worker has
// returned its proof. It is an operational bound, not a cryptographic input.
func (s Spec) ProofReserveBytes() int {
	switch s.CertificateProfile {
	case "zk-ezkl-diffusion-v1":
		return 128 << 10
	case "aux-ed25519-v1":
		return 1024
	default:
		return 64
	}
}

//go:embed *.json
var fixtures embed.FS

// Fixture loads a shipped profile implemented by the Go deterministic worker.
func Fixture(id string) (Spec, error) {
	spec, err := Load(id)
	if err != nil {
		return Spec{}, err
	}
	if spec.BackendName() != "fixture" {
		return Spec{}, fmt.Errorf("profile %q is not a fixture", id)
	}
	return spec, nil
}

// Load reads any shipped model profile by its consensus ID.
func Load(id string) (Spec, error) {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return Spec{}, fmt.Errorf("invalid profile ID %q", id)
	}
	data, err := fixtures.ReadFile(id + ".json")
	if err != nil {
		return Spec{}, err
	}
	var s Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return Spec{}, err
	}
	return s, s.Validate()
}
