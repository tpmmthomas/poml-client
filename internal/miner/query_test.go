// The honest baseline samples the eligible, retrievable set again on every attempt.
package miner

import (
	"fmt"
	"testing"

	"github.com/thomas/poml-node/internal/types"
)

func TestPerAttemptQuerySelection(t *testing.T) {
	m, r, _, _ := testRound(t)
	current := types.NewState()
	current.Accounts["u"] = types.Account{Balance: 5}
	for _, id := range []string{"a", "b", "pending", "expired", "unfunded", "missing"} {
		q := r.queries[0]
		q.ID = id
		switch id {
		case "expired":
			q.Expiry = 2
		case "unfunded":
			q.Fee = 8
		case "missing":
			q.Input = nil
		}
		current.Queries[id] = q
	}
	current.Claims["pending"] = types.Claim{Status: types.ClaimChallenged}
	current.Claims["b"] = types.Claim{Status: types.ClaimReopened}
	queries, err := eligibleQueries(current, nil, 3, m.config.Genesis)
	if err != nil || len(queries) != 2 || queries[0].ID != "a" || queries[1].ID != "b" {
		t.Fatalf("eligible queries %v, err %v", queries, err)
	}
	r.queries = queries
	counts := map[string]int{}
	for i := 1; i <= 64; i++ {
		id := fmt.Sprintf("attempt-%d", i)
		pick := r.query(id).ID
		if r.query(id).ID != pick {
			t.Fatal("same attempt changed its query")
		}
		counts[pick]++
	}
	if counts["a"] == 0 || counts["b"] == 0 {
		t.Fatal("round pinned all attempts to a single query")
	}
}
