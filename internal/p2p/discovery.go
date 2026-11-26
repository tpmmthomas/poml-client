// Discovery starts from operator seeds, exchanges bounded address lists and
// persists learned endpoints so a seed may go offline between restarts.
package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type dialState struct {
	busy     bool
	failures uint
	next     time.Time
}

func validAddress(address string) bool {
	if len(address) > 255 || strings.IndexFunc(address, func(r rune) bool { return r <= ' ' || r == '/' || r == '\\' }) >= 0 {
		return false
	}
	host, port, err := net.SplitHostPort(address)
	n, errPort := strconv.ParseUint(port, 10, 16)
	return err == nil && host != "" && errPort == nil && n > 0 && !net.ParseIP(host).IsUnspecified()
}

// Called under the server lock at startup or when a session shares addresses.
func (s *Server) remember(address string) bool {
	if !validAddress(address) || address == s.config.Advertise || address == s.config.Listen ||
		s.addresses[address] != nil || len(s.addresses) >= maxAddresses {
		return false
	}
	s.addresses[address] = &dialState{}
	return true
}

func (s *Server) loadAddresses() error {
	for _, seed := range s.config.Seeds {
		if !validAddress(seed) {
			return fmt.Errorf("invalid seed address %q", seed)
		}
		s.remember(seed)
	}
	if s.config.AddressBook == "" {
		return nil
	}
	data, err := os.ReadFile(s.config.AddressBook)
	if errors.Is(err, os.ErrNotExist) {
		return s.saveAddresses()
	}
	if err != nil {
		return err
	}
	var addresses []string
	if err := json.Unmarshal(data, &addresses); err != nil {
		return fmt.Errorf("read P2P address book: %w", err)
	}
	for _, address := range addresses {
		s.remember(address)
	}
	return s.saveAddresses()
}

func (s *Server) listAddresses() []string {
	addresses := make([]string, 0, len(s.addresses))
	for address := range s.addresses {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	return addresses
}

func (s *Server) learn(addresses []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, address := range addresses {
		if s.remember(address) {
			changed = true
		}
	}
	if !changed || s.config.AddressBook == "" {
		return nil
	}
	return s.saveAddresses()
}

// Atomic replacement prevents readers from observing a partial address list.
func (s *Server) saveAddresses() error {
	data, err := json.Marshal(s.listAddresses())
	if err != nil {
		return err
	}
	path := s.config.AddressBook
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (s *Server) dialLoop(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.dialDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) dialDue(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	connected := make(map[string]bool, len(s.peers))
	for _, p := range s.peers {
		connected[p.remote.Address] = true
	}
	for address, state := range s.addresses {
		if state.busy || time.Now().Before(state.next) || connected[address] {
			continue
		}
		// Reserve half the session budget for listeners. A full list of
		// unavailable addresses cannot occupy every inbound handshake slot.
		if len(s.slots) >= maxPeers/2 {
			return
		}
		select {
		case s.slots <- struct{}{}:
		default:
			return
		}
		s.sessions.Add(1)
		state.busy = true
		go func() {
			defer s.release()
			conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
			ok := err == nil && s.serve(ctx, conn, address)
			s.mu.Lock()
			defer s.mu.Unlock()
			state.busy = false
			if ok {
				state.failures = 0
			} else if state.failures < 7 {
				state.failures++
			}
			state.next = time.Now().Add((250 * time.Millisecond) << state.failures)
		}()
	}
}
