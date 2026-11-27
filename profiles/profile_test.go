// A genesis profile controls the ticket weight and work request shape at every node.
package profiles

import (
	"testing"

	"github.com/thomas/poml-node/internal/types"
)

func TestProfileBoundsAndDigest(t *testing.T) {
	a, err := Fixture("fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fixture("fixture-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() == b.Digest() {
		t.Fatal("fixture profiles collided")
	}
	changed := a
	changed.RandomnessSlots++
	if changed.Digest() == a.Digest() {
		t.Fatal("digest omitted randomness shape")
	}
	changed = a
	changed.TicketWeight = 1 << 32
	if changed.Validate() == nil {
		t.Fatal("accepted an impossible lottery weight")
	}
	changed = a
	changed.RandomnessBytes = 255*32 + 1
	if changed.Validate() == nil {
		t.Fatal("accepted an impossible HKDF output length")
	}
	changed = a
	changed.ModelDigest = "invalid"
	if changed.Validate() == nil {
		t.Fatal("accepted invalid model digest")
	}
	if _, err := Fixture("../secrets"); err == nil {
		t.Fatal("accepted unknown bundled profile")
	}
}

func TestStage2ProfilesAndProofMode(t *testing.T) {
	for _, id := range []string{"dummy", "diffusion-small", "gemma3-1b"} {
		spec, err := Load(id)
		if err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if !spec.AcceptsRandomProof() || spec.BackendName() == "" || spec.Digest() == (types.Hash{}) {
			t.Fatalf("invalid stage 2 profile %s", id)
		}
	}
	for _, id := range []string{"fixture-a-aux", "diffusion-small-aux", "diffusion-small-ezkl", "gemma3-1b-aux"} {
		spec, err := Load(id)
		if err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if spec.AcceptsRandomProof() || !spec.UsesProofBundle() || spec.Digest() == (types.Hash{}) {
			t.Fatalf("invalid proof-bearing profile %s", id)
		}
	}
	fixture, err := Fixture("fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	if fixture.AcceptsRandomProof() || fixture.BackendName() != "fixture" {
		t.Fatal("fixture profile unexpectedly accepts dummy proof")
	}
}
