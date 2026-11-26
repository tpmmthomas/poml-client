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
}

// Validate bounds computation before the scheduler or verifier accepts the profile.
func (s Spec) Validate() error {
	if s.ID == "" || s.CertificateProfile == "" {
		return errors.New("model and certificate profile IDs are required")
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
	return types.HashBytes("worker-profile", []byte(s.ID), []byte(s.BackendName()), []byte(modelRef), []byte(s.ModelDigest), []byte(s.CertificateProfile), numbers)
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

//go:embed *.json
var fixtures embed.FS

// Fixture loads one of the two shipped deterministic worker profiles.
func Fixture(id string) (Spec, error) {
	if id != "fixture-a" && id != "fixture-b" {
		return Spec{}, fmt.Errorf("unknown fixture %q", id)
	}
	return Load(id)
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
