// A genesis profile controls the ticket weight and work request shape at every node.
package profiles

import "testing"

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
