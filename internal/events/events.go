// Package events writes the append-only JSONL ledger used for experiments.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type Writer struct {
	mu       sync.Mutex
	file     *os.File
	started  time.Time
	sequence uint64
	base     map[string]any
}

func New(path string, base map[string]any) (*Writer, error) {
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	copyBase := make(map[string]any, len(base)+1)
	for key, value := range base {
		copyBase[key] = value
	}
	copyBase["boot_id"] = randomID()
	return &Writer{file: file, started: time.Now(), base: copyBase}, nil
}

func (w *Writer) Close() error { return w.file.Close() }

func (w *Writer) Emit(eventType, status, reason string, fields map[string]any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sequence++
	record := make(map[string]any, len(w.base)+len(fields)+8)
	for key, value := range w.base {
		record[key] = value
	}
	for key, value := range fields {
		record[key] = value
	}
	record["local_sequence"] = w.sequence
	record["monotonic_ns"] = time.Since(w.started).Nanoseconds()
	record["wall_time"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["event_type"] = eventType
	record["status"] = status
	record["reason"] = reason
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := w.file.Write(append(data, '\n')); err != nil {
		return err
	}
	return w.file.Sync()
}

func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}

func randomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("boot-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
