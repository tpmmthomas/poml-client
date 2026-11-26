// Deferred responses keep their signed original context even if the parent changes.
package miner

import (
	"errors"
	"time"

	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/types"
)

// PoolResponse owns the durable local queue. If a storage write fails the
// scheduler keeps the signed transaction and retries while the process lives.
func (m *Miner) queue(tx types.Tx, reason string) {
	id := codec.TxID(tx)
	fields := map[string]any{
		"parent_hash": tx.Parent.String(), "prefix_digest": tx.PrefixDigest.String(),
		"qid": tx.QueryID, "attempt_id": tx.AttemptID, "tx_id": id.String(),
		"created_height": tx.CreatedAt,
	}
	var err error
	if m.config.PoolResponse == nil {
		err = errors.New("missing durable response queue")
	} else {
		err = m.config.PoolResponse(tx)
	}
	if err != nil {
		m.unsaved[id] = tx
		fields["pending_storage"] = len(m.unsaved)
		m.record("response_queue", "retry", err.Error(), fields)
		return
	}
	delete(m.unsaved, id)
	m.record("response_queue", "queued", reason, fields)
}

func (m *Miner) retryQueue() {
	if len(m.unsaved) == 0 || time.Now().Before(m.nextRetry) {
		return
	}
	m.nextRetry = time.Now().Add(time.Second)
	for _, tx := range m.unsaved {
		m.queue(tx, "retry durable queue")
	}
}
