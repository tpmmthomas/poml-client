// A real fixture process can be killed mid-request and restarted on the same socket.
package worker

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWorkerProcessRecovery(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "pomlworker")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "../../cmd/pomlworker")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %s, %v", output, err)
	}
	keyFile := filepath.Join(dir, "test-key")
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{11}, 32))
	if err := os.WriteFile(keyFile, []byte(hex.EncodeToString(key)), 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "w.sock")
	start := func(delay string) *exec.Cmd {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, "--key-file", keyFile, "--socket", socket, "--inference-delay", delay)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			conn, err := net.DialTimeout("unix", socket, 50*time.Millisecond)
			if err == nil {
				conn.Close()
				return cmd
			}
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatal("worker failed to listen")
			}
			time.Sleep(time.Millisecond)
		}
	}
	slow := start("5s")
	c := Client{Socket: socket}
	result := make(chan error, 1)
	req := inferenceInput(t, "fixture-a")
	go func() { _, err := c.Infer(t.Context(), req); result <- err }()
	time.Sleep(30 * time.Millisecond)
	if err := slow.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = slow.Wait() // SIGKILL intentionally leaves the old socket path on disk.
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("dead worker answered inference")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request hung after worker died")
	}
	restarted := start("0s")
	t.Cleanup(func() { _ = restarted.Process.Kill(); _ = restarted.Wait() })
	for _, id := range []string{"fixture-a", "fixture-b"} {
		req := inferenceInput(t, id)
		got, err := c.Infer(t.Context(), req)
		if err != nil || len(got.Output) != int(req.Spec.OutputBytes) {
			t.Fatalf("restart %s: %v", id, err)
		}
	}
}
