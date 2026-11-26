// Published vectors catch a sender and a recipient making the same implementation mistake.
package crypto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/profiles"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRFC9381Example19(t *testing.T) {
	seed := unhex(t, "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	output, proof, pub, err := VRFProve(seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(pub) != "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a" ||
		hex.EncodeToString(proof) != "7d9c633ffeee27349264cf5c667579fc583b4bda63ab71d001f89c10003ab46f14adf9a3cd8b8412d9038531e865c341cafa73589b023d14311c331a9ad15ff2fb37831e00f0acaa6d73bc9997b06501" ||
		hex.EncodeToString(output) != "9d574bf9b8302ec0fc1e21c3ec5368269527b87b462ce36dab2d14ccf80c53cccf6758f058c5b1c856b116388152bbe509ee3b9ecfe63d93c3b4346c1fbc6c54" {
		t.Fatal("RFC 9381 B.4 example 19 mismatch")
	}
}

func TestCFRGHPKEBase(t *testing.T) {
	data, err := os.ReadFile("testdata/hpke-base.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	field := func(k string) []byte { return unhex(t, v[k]) }
	ciphertext, err := sealWithSeed(field("pkRm"), field("ikmE"), field("info"), field("aad"), field("pt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ciphertext, append(field("enc"), field("ct")...)) {
		t.Fatalf("HPKE CFRG vector mismatch: %x", ciphertext)
	}
	plain, err := DecryptDeterministic(field("skRm"), field("info"), field("aad"), ciphertext)
	if err != nil || !bytes.Equal(plain, field("pt")) {
		t.Fatalf("recipient: %x, %v", plain, err)
	}
}

func TestOrderedRandomness(t *testing.T) {
	spec, _ := profiles.Fixture("fixture-b")
	rho := bytes.Repeat([]byte{1}, 64)
	a, err := ExpandRandomness(spec, []byte("parent-one"), rho)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ExpandRandomness(spec, []byte("parent-two"), rho)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 3 || len(a[0]) != 32 || bytes.Equal(a[0], a[1]) || bytes.Equal(a[0], b[0]) {
		t.Fatal("expanded randomness did not bind slot and context")
	}
}

func TestPoMLPrescribedCoins(t *testing.T) {
	var v map[string]string
	data, err := os.ReadFile("testdata/poml-output.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	parent, err := types.ParseHash(v["parent"])
	if err != nil {
		t.Fatal(err)
	}
	winner := types.Winner{ModelID: v["profile"], Parent: parent, QueryID: v["qid"], AttemptID: v["attempt_id"]}
	ciphertext, err := SealOutput(unhex(t, v["recipient_public"]), unhex(t, v["encryption_vrf"]), v["chain_id"], winner, unhex(t, v["output"]))
	if err != nil || hex.EncodeToString(ciphertext) != v["ciphertext"] {
		t.Fatalf("mismatched independent PoML vector: %x %v", ciphertext, err)
	}
	winner.Ciphertext = ciphertext
	opened, err := OpenOutput(unhex(t, v["recipient_private"]), v["chain_id"], winner)
	if err != nil || hex.EncodeToString(opened) != v["output"] {
		t.Fatalf("round trip: %x %v", opened, err)
	}
	if _, err := OpenOutput(unhex(t, v["recipient_private"]), "different", winner); err == nil {
		t.Fatal("wrong chain opened ciphertext")
	}
}
