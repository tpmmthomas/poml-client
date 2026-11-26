// Observation order is local. Peers only validate the signed block that results.
package miner

import "github.com/thomas/poml-node/internal/types"

type observedResults struct {
	sequence            uint64
	consecutiveFailures int
	winner              *attemptResult
	losses              []types.Tx
	responded           map[string]struct{}
}

// observe returns stop on the first hit or a full set of failed worker slots.
// The scheduler continues draining already dispatched results in either case.
func (m *Miner) observe(r miningRound, s *observedResults, result attemptResult, stopped bool) (stop bool, err error) {
	if s.responded == nil {
		s.responded = make(map[string]struct{})
	}
	s.sequence++
	w := result.candidate
	fields := map[string]any{
		"parent_hash": r.parent.String(), "prefix_digest": r.digest.String(), "attempt_id": w.AttemptID,
		"qid": w.QueryID, "ticket_hash": w.TicketHash.String(), "completion_sequence": s.sequence, "C_ticket": w.CTicket,
	}
	if result.err != nil {
		kind := "attempt_failure"
		if stopped {
			kind = "attempt_cancel"
		}
		m.record(kind, "failed", result.err.Error(), fields)
		s.consecutiveFailures++
		if !stopped && s.consecutiveFailures >= m.config.Parallelism {
			return true, result.err
		}
		return false, nil
	}
	s.consecutiveFailures = 0
	kind := "ticket_miss"
	if result.hit {
		kind = "ticket_hit"
	}
	m.record(kind, "complete", "candidate evaluated", fields)
	switch {
	case stopped:
		if result.hit || s.winner != nil && s.winner.candidate.QueryID == w.QueryID {
			m.record("response_duplicate", "discarded", "winner already owns this query", fields)
			break
		}
		if _, exists := s.responded[w.QueryID]; exists {
			m.record("response_duplicate", "discarded", "a response for this query was already observed", fields)
			break
		}
		s.responded[w.QueryID] = struct{}{}
		m.record("attempt_stale", "complete", "result observed after seal or parent replacement", fields)
		m.queue(m.response(r.height, s.sequence, w), "post-seal or stale parent")
	case result.hit:
		if _, exists := s.responded[w.QueryID]; exists {
			filtered := s.losses[:0]
			for _, loss := range s.losses {
				if loss.QueryID != w.QueryID {
					filtered = append(filtered, loss)
				}
			}
			s.losses = filtered
			delete(s.responded, w.QueryID)
			m.record("response_superseded", "discarded", "winning attempt replaced an earlier local response", fields)
		}
		s.winner = &result
		m.record("seal", "complete", "first winning candidate observed", fields)
		return true, nil
	default:
		if s.responded == nil {
			s.responded = make(map[string]struct{})
		}
		if _, exists := s.responded[w.QueryID]; exists {
			m.record("response_duplicate", "discarded", "a response for this query was already observed", fields)
			break
		}
		s.responded[w.QueryID] = struct{}{}
		if uint64(len(s.losses)) < m.config.Genesis.Raw.ResponseLimit {
			s.losses = append(s.losses, m.response(r.height, s.sequence, w))
		} else {
			m.queue(m.response(r.height, s.sequence, w), "response count overflow")
		}
	}
	return false, nil
}
