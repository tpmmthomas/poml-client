// Relay and initial mempool reconciliation share a small download window.
// With two outstanding requests the sender can buffer full-size bodies inside
// its byte budget. A reconnect resumes from whatever the node durably holds.
package p2p

import (
	"errors"
	"time"

	"github.com/thomas/poml-node/internal/p2p/wire"
)

func (p *peer) request(k objectKey) error {
	if _, pending := p.pending[k]; pending || p.server.has(k) {
		return nil
	}
	for _, queued := range p.want {
		if k == queued {
			return nil
		}
	}
	if len(p.want) >= 2*maxInventory {
		return errors.New("peer inventory backlog full")
	}
	p.want = append(p.want, k)
	return nil
}

func (p *peer) pump() error {
	// Another session may already have delivered a requested header's body.
	if len(p.page) > 0 {
		if err := p.nextBody(); err != nil {
			return err
		}
	}
	for len(p.pending) < 2 && len(p.want) > 0 {
		k := p.want[0]
		p.want = p.want[1:]
		if p.server.has(k) {
			continue
		}
		p.pending[k] = time.Now()
		if err := p.send(wire.Frame_GET_DATA, ref(k.category, k.id)); err != nil {
			return err
		}
	}
	if p.morePool && len(p.pending)+len(p.want) == 0 {
		p.morePool = false
		p.askMempool()
	}
	return nil
}

func (p *peer) readInventory(f *wire.Frame) error {
	var inv wire.Inventory
	if err := decode(f.Payload, &inv); err != nil {
		return err
	}
	if len(inv.Items) > maxInventory {
		return errors.New("oversized inventory")
	}
	if f.Kind == wire.Frame_MEMPOOL {
		if inv.More && len(inv.Items) == 0 {
			return errors.New("empty intermediate mempool page")
		}
		p.morePool = inv.More
	}
	for _, r := range inv.Items {
		k, err := key(r)
		if err != nil {
			return err
		}
		if f.Kind == wire.Frame_MEMPOOL && k.category != wire.ObjectRef_TX {
			return errors.New("non-transaction in mempool inventory")
		}
		if k.category == wire.ObjectRef_INPUT && !p.server.wantsInput(k.id) {
			continue
		}
		if err := p.request(k); err != nil {
			return err
		}
	}
	return nil
}

func (p *peer) askMempool() {
	p.waitingPool = time.Now()
	p.enqueue(raw(wire.Frame_GET_MEMPOOL, nil))
}

func (p *peer) shareMempool() error {
	if !p.poolLoaded {
		if p.server.callbacks.PendingTxIDs != nil {
			p.poolSnapshot = p.server.callbacks.PendingTxIDs()
		}
		p.poolLoaded = true
	}
	count := min(maxInventory, len(p.poolSnapshot))
	page := &wire.Inventory{More: count < len(p.poolSnapshot)}
	for _, id := range p.poolSnapshot[:count] {
		page.Items = append(page.Items, ref(wire.ObjectRef_TX, id))
	}
	p.poolSnapshot = p.poolSnapshot[count:]
	if len(p.poolSnapshot) == 0 {
		p.poolSnapshot = nil
	}
	return p.send(wire.Frame_MEMPOOL, page)
}
