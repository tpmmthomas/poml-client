package consensus

import (
	"math/big"
	"testing"

	"github.com/thomas/poml-node/internal/types"
)

func TestThresholdWeightOne(t *testing.T) {
	base := new(big.Int).Lsh(big.NewInt(1), 248)
	threshold, err := Threshold(256, base, 1)
	if err != nil {
		t.Fatal(err)
	}
	if threshold.Cmp(base) != 0 {
		t.Fatalf("threshold %s, want %s", threshold, base)
	}
	var below types.Hash
	below[0] = 0
	if !TicketWins(below, threshold) {
		t.Fatal("zero hash should win")
	}
	var belowBoundary types.Hash
	for i := range belowBoundary {
		belowBoundary[i] = 0xff
	}
	belowBoundary[0] = 0
	if !TicketWins(belowBoundary, threshold) {
		t.Fatal("hash below 2^248 should win")
	}
	var at types.Hash
	at[0] = 1
	if TicketWins(at, threshold) {
		t.Fatal("strict boundary hash should miss")
	}
	var above types.Hash
	above[0] = 1
	above[1] = 1
	if TicketWins(above, threshold) {
		t.Fatal("hash above 2^248 should miss")
	}
}
