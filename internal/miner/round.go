// One scheduler serializes observation of competing local attempt completions.
package miner

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/thomas/poml-node/internal/profile"
	"github.com/thomas/poml-node/internal/state"
	"github.com/thomas/poml-node/internal/types"
)

type miningRound struct {
	parent, digest types.Hash
	height         uint64
	prefix         []types.Tx
	queries        []types.Query
}

func eligibleQueries(snapshot types.State, prefix []types.Tx, height uint64, g profile.Genesis) ([]types.Query, error) {
	current := snapshot.Copy()
	for _, tx := range prefix {
		if err := state.ApplyTransaction(current, tx, height, g); err != nil {
			return nil, fmt.Errorf("unexecutable prefix: %w", err)
		}
	}
	var queries []types.Query
	for _, q := range current.Queries {
		if q.Settled || q.Expired || q.Expiry < height || len(q.Input) == 0 || q.ModelID != g.Raw.ModelProfileID {
			continue
		}
		if claim, ok := current.Claims[q.ID]; ok && (claim.Status == types.ClaimPending || claim.Status == types.ClaimChallenged || (claim.Status == 0 && !claim.Settled)) {
			continue
		}
		if q.Escrow < q.Fee && current.Accounts[q.Sender].Balance < q.Fee-q.Escrow {
			continue
		}
		if err := state.ValidateQueryCommitment(q); err != nil {
			continue
		}
		queries = append(queries, q)
	}
	sort.Slice(queries, func(i, j int) bool { return queries[i].ID < queries[j].ID })
	return queries, nil
}

// query uses a public reproducible baseline sampler. Each persisted attempt
// gets its own independent choice over the sorted eligible set.
func (r miningRound) query(id string) types.Query {
	seed := types.HashBytes("query-selection", r.parent[:], r.digest[:], []byte(id))
	return r.queries[rand.New(rand.NewChaCha8(seed)).IntN(len(r.queries))]
}

func (m *Miner) launch(ctx context.Context, r miningRound, results chan<- attemptResult) error {
	if m.config.NextAttempt == nil {
		return errors.New("missing durable attempt counter")
	}
	n, err := m.config.NextAttempt()
	if err != nil {
		return fmt.Errorf("reserve attempt: %w", err)
	}
	id := fmt.Sprintf("%s-%d", m.config.MinerID, n)
	q := r.query(id)
	m.record("worker_queue", "started", "attempt number reserved", map[string]any{
		"parent_hash": r.parent.String(), "prefix_digest": r.digest.String(), "attempt_id": id,
		"request_id": "mining/" + id, "qid": q.ID, "height": r.height,
	})
	go func() { results <- m.attempt(ctx, r.parent, r.digest, q, id, "mining") }()
	return nil
}

func (m *Miner) mineRound(ctx context.Context, r miningRound) error {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan attemptResult, m.config.Parallelism)
	var seal observedResults
	var active int
	var dispatched uint64
	var runErr error
	for {
		for work.Err() == nil && active < m.config.Parallelism {
			if err := m.launch(work, r, results); err != nil {
				runErr = err
				cancel()
				break
			}
			active++
			dispatched++
			if m.config.MaxAttempts != 0 && dispatched%m.config.MaxAttempts == 0 {
				m.record("attempt_batch", "complete", "continuing frozen round", map[string]any{
					"parent_hash": r.parent.String(), "prefix_digest": r.digest.String(),
					"dispatched": dispatched,
				})
			}
		}
		if active == 0 {
			break
		}
		result := <-results
		active--
		if stop, err := m.observe(r, &seal, result, work.Err() != nil); stop {
			if err != nil {
				runErr = err
			}
			cancel()
		}
	}
	if err := ctx.Err(); err != nil {
		runErr = err
	}
	if seal.winner == nil || runErr != nil {
		for _, tx := range seal.losses {
			m.queue(tx, "interrupted round")
		}
		return runErr
	}
	if err := m.publish(ctx, r, *seal.winner, seal.losses); err != nil {
		for _, tx := range seal.losses {
			m.queue(tx, "block publication failed")
		}
		return err
	}
	return nil
}
