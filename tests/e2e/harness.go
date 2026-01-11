package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type systemUnderTest struct {
	BaseURL  string
	shutdown func()
	restart  func(t *testing.T)
}

func (s *systemUnderTest) Close() {
	if s.shutdown != nil {
		s.shutdown()
	}
}

func startSystemUnderTest(t *testing.T) *systemUnderTest {
	t.Helper()

	if cmd := os.Getenv("KV_SERVER_CMD"); cmd != "" {
		sut, err := startExternalServer(t, cmd)
		if err != nil {
			t.Fatalf("start external server: %v", err)
		}
		return sut
	}

	if url := os.Getenv("KV_SERVER_URL"); url != "" {
		t.Logf("KV_SERVER_URL set; using existing server at %s", url)
		return &systemUnderTest{
			BaseURL: url,
			shutdown: func() {
				// External server; nothing to stop.
			},
			restart: nil, // restart not supported without process control
		}
	}

	// Fallback stub that returns 501 so tests fail until a real server is wired in.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"message":"server not implemented"}`))
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("stub listen: %v", err)
	}
	stub := httptest.NewUnstartedServer(handler)
	stub.Listener = l
	stub.Start()

	t.Log("KV_SERVER_URL not set; using stub server (expected to fail tests until real server exists)")

	return &systemUnderTest{
		BaseURL:  stub.URL,
		shutdown: stub.Close,
		restart:  nil,
	}
}

func startExternalServer(t *testing.T, cmdStr string) (*systemUnderTest, error) {
	t.Helper()

	dataDir, err := os.MkdirTemp("", "simplekv-e2e-data-*")
	if err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	addr, err := freeAddr()
	if err != nil {
		return nil, fmt.Errorf("pick free addr: %w", err)
	}

	launcher := func() (*exec.Cmd, string, error) {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("KV_HTTP_ADDR=%s", addr),
			fmt.Sprintf("KV_DATA_DIR=%s", dataDir),
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			cancel()
			return nil, "", fmt.Errorf("cmd start: %w", err)
		}
		baseURL := "http://" + addr
		if err := waitForReady(baseURL, 10*time.Second); err != nil {
			_ = cmd.Process.Kill()
			cancel()
			return nil, "", fmt.Errorf("wait for ready: %w", err)
		}
		return cmd, baseURL, nil
	}

	cmd, baseURL, err := launcher()
	if err != nil {
		return nil, err
	}

	restart := func(t *testing.T) {
		t.Helper()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}

		newCmd, _, err := launcher()
		if err != nil {
			t.Fatalf("restart server: %v", err)
		}
		cmd = newCmd
	}

	shutdown := func() {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		_ = os.RemoveAll(dataDir)
	}

	return &systemUnderTest{
		BaseURL:  baseURL,
		shutdown: shutdown,
		restart:  restart,
	}, nil
}

func waitForReady(baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		if strings.Contains(err.Error(), "connection refused") {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server at %s not ready after %s", baseURL, timeout)
}

func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}
