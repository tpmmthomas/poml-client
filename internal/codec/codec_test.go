package codec

import (
	"testing"

	"github.com/thomas/poml-node/internal/types"
)

func TestBlockRoundTrip(t *testing.T) {
	tx := types.Tx{Kind: types.TxQuery, Sender: "user", QueryID: "q1", Input: []byte("input"), ModelID: "fixture-a"}
	tx.Signature = []byte("signature")
	block := types.Block{
		Header: types.Header{Height: 1, Producer: "miner"}, Prefix: []types.Tx{tx},
		Winner: types.Winner{QueryID: "q1", AttemptID: "a1", MinerID: "miner", ModelID: "fixture-a", Ciphertext: []byte("cipher")},
	}
	encoded := BlockBytes(block)
	decoded, err := DecodeBlock(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(BlockBytes(decoded)) != string(encoded) {
		t.Fatal("block encoding is not stable")
	}
	if BlockID(decoded) != BlockID(block) {
		t.Fatal("block ID changed after round trip")
	}
}
