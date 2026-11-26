// Package profiles defines the model-independent worker contract pinned in genesis.
package profiles

import (
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thomas/poml-node/internal/types"
)

// Spec fixes resource bounds, the ordered randomness transcript and the public
// ticket weight. A node needs these fields to dispatch work and validate blocks.
type Spec struct {
	ID                 string `json:"id"`
	ModelDigest        string `json:"model_digest"`
	RandomnessSlots    uint32 `json:"randomness_slots"`
	RandomnessBytes    uint32 `json:"randomness_bytes"`
	OutputBytes        uint32 `json:"output_bytes"`
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
	if s.RandomnessSlots < 1 || s.RandomnessSlots > 256 || s.RandomnessBytes < 1 || s.RandomnessBytes > 255*32 {
		return errors.New("invalid randomness slot dimensions")
	}
	if s.OutputBytes < 1 || s.OutputBytes > 4<<20 || s.TicketWeight < 1 || s.TicketWeight > 4096 || s.MaxRuntimeMS < 1 || s.MaxRuntimeMS > 3600000 {
		return errors.New("invalid output, ticket weight or runtime bound")
	}
	return nil
}

// Digest binds the worker handshake to the complete profile, independent of JSON layout.
func (s Spec) Digest() types.Hash {
	var numbers []byte
	for _, n := range []uint64{uint64(s.RandomnessSlots), uint64(s.RandomnessBytes), uint64(s.OutputBytes), s.TicketWeight, uint64(s.MaxRuntimeMS)} {
		numbers = binary.BigEndian.AppendUint64(numbers, n)
	}
	return types.HashBytes("worker-profile", []byte(s.ID), []byte(s.ModelDigest), []byte(s.CertificateProfile), numbers)
}

//go:embed fixture-a.json fixture-b.json
var fixtures embed.FS

// Fixture loads one of the two shipped deterministic worker profiles.
func Fixture(id string) (Spec, error) {
	if id != "fixture-a" && id != "fixture-b" {
		return Spec{}, fmt.Errorf("unknown fixture %q", id)
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
