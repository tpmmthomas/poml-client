package store

import (
	"bytes"
	"testing"

	"github.com/thomas/poml-node/internal/types"
)

func TestGenesisTipAndInputSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	genesis := types.HashBytes("genesis-test", []byte("one"))
	input := []byte("query input")
	commitment := types.HashBytes("input", input)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutGenesis(genesis, types.NewState()); err != nil {
		t.Fatal(err)
	}
	if err := db.PutInput(commitment, input); err != nil {
		t.Fatal(err)
	}
	if got, err := db.Tip(); err != nil || got != genesis {
		t.Fatalf("genesis tip = %v, err = %v", got, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got, err := db.Genesis(); err != nil || got != genesis {
		t.Fatalf("stored genesis = %v, err = %v", got, err)
	}
	gotInput, err := db.Input(commitment)
	if err != nil || !bytes.Equal(gotInput, input) {
		t.Fatalf("stored input = %q, err = %v", gotInput, err)
	}
}
