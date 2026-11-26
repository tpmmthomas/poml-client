// Synchronization is independent of mining. A peer sends an exponentially
// spaced locator, downloads short pages of block IDs, then requests and checks
// each full body in order. Side branches stay available by ID.
package p2p

import (
	"bytes"
	"errors"
	"time"

	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/p2p/wire"
	"github.com/thomas/poml-node/internal/types"
)

func point(id types.Hash, height uint64) *wire.Point { return &wire.Point{Id: id[:], Height: height} }

func blockPoint(data []byte) (*wire.Point, error) {
	b, err := codec.DecodeBlock(data)
	if err != nil {
		return nil, err
	}
	h := point(codec.BlockID(b), b.Header.Height)
	h.Parent = b.Header.Parent[:]
	return h, nil
}

func (s *Server) canonicalPoint(height uint64) (*wire.Point, error) {
	if height == 0 {
		return point(s.config.GenesisHash, 0), nil
	}
	blocks, err := s.callbacks.CanonicalBlocks(height, 1)
	if err != nil || len(blocks) == 0 {
		return nil, err
	}
	return blockPoint(blocks[0])
}

func (s *Server) locator() ([]*wire.Point, error) {
	hash, height := s.callbacks.Tip()
	points := []*wire.Point{point(hash, height)}
	step := uint64(1)
	for height > 0 {
		height -= min(height, step)
		h, err := s.canonicalPoint(height)
		if err != nil {
			return nil, err
		}
		if h != nil {
			points = append(points, h)
		}
		if len(points) >= 8 && step <= 1<<62 {
			step *= 2
		}
	}
	return points, nil
}

func (p *peer) onStatus(tip *wire.Point) error {
	if len(tip.Id) != types.HashSize || (tip.Height == 0 && !bytes.Equal(tip.Id, p.server.config.GenesisHash[:])) {
		return errors.New("invalid peer status")
	}
	p.remoteTip = tip
	_, localHeight := p.server.callbacks.Tip()
	if !p.waitingHeaders.IsZero() || len(p.page) > 0 || tip.Height < localHeight || p.server.has(objectKey{wire.ObjectRef_BLOCK, types.Hash(tip.Id)}) {
		return nil
	}
	locator, err := p.server.locator()
	if err != nil {
		return err
	}
	return p.requestHeaders(locator)
}

func (p *peer) requestHeaders(locator []*wire.Point) error {
	p.lastLocator = locator
	p.waitingHeaders = time.Now()
	return p.send(wire.Frame_GET_HEADERS, &wire.Points{Items: locator})
}

func (p *peer) serveHeaders(locator []*wire.Point) error {
	if len(locator) == 0 || len(locator) > maxLocator {
		return errors.New("invalid locator length")
	}
	_, localHeight := p.server.callbacks.Tip()
	for _, candidate := range locator {
		if candidate == nil || len(candidate.Id) != types.HashSize {
			return errors.New("invalid locator entry")
		}
		if candidate.Height > localHeight {
			continue
		}
		ours, err := p.server.canonicalPoint(candidate.Height)
		if err != nil {
			return err
		}
		if ours == nil || !bytes.Equal(candidate.Id, ours.Id) {
			continue
		}
		page := &wire.Points{}
		if candidate.Height < localHeight {
			blocks, err := p.server.callbacks.CanonicalBlocks(candidate.Height+1, pageSize)
			if err != nil {
				return err
			}
			for _, data := range blocks {
				h, err := blockPoint(data)
				if err != nil {
					return err
				}
				page.Items = append(page.Items, h)
			}
			// Fork choice may have changed between independent read snapshots.
			// An empty reply lets the next status exchange restart the locator.
			if len(page.Items) > 0 && !bytes.Equal(page.Items[0].Parent, candidate.Id) {
				page.Items = nil
			}
		}
		return p.send(wire.Frame_HEADERS, page)
	}
	return errors.New("locator has no common genesis")
}

func (p *peer) readHeaders(page []*wire.Point) error {
	if p.waitingHeaders.IsZero() {
		return errors.New("unsolicited header page")
	}
	p.waitingHeaders = time.Time{}
	if len(page) > pageSize {
		return errors.New("oversized header page")
	}
	if len(page) == 0 {
		return nil
	}
	first := page[0]
	if first == nil || first.Height == 0 {
		return errors.New("invalid start of header page")
	}
	connected := false
	for _, ancestor := range p.lastLocator {
		if first.Height-1 == ancestor.Height && bytes.Equal(first.Parent, ancestor.Id) {
			connected = true
			break
		}
	}
	if !connected {
		return errors.New("header page does not link to locator")
	}
	parent, height := first.Parent, first.Height
	for _, h := range page {
		if h == nil || len(h.Id) != types.HashSize || len(h.Parent) != types.HashSize || h.Height != height || !bytes.Equal(h.Parent, parent) {
			return errors.New("unordered or incomplete header page")
		}
		parent = h.Id
		height++
	}
	p.page = page
	return p.nextBody()
}

func (p *peer) nextBody() error {
	if len(p.page) == 0 {
		return nil
	}
	last := p.page[len(p.page)-1]
	for len(p.page) > 0 {
		h := p.page[0]
		k := objectKey{wire.ObjectRef_BLOCK, types.Hash(h.Id)}
		if !p.server.has(k) {
			return p.request(k)
		}
		p.page = p.page[1:]
	}
	if p.remoteTip != nil && last.Height < p.remoteTip.Height {
		return p.requestHeaders([]*wire.Point{last, point(p.server.config.GenesisHash, 0)})
	}
	return nil
}
