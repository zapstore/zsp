//go:build release && !race

package release

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const fixturePubkey = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

// relayLogs is the local relay's stdout, kept so a failed publish can show the relay's reason.
var relayLogs *strings.Builder

func startLocalRelay(binary string) (relayURL, blossomURL, socketPath string, stop func(), err error) {
	stop = func() {}
	dir, err := os.MkdirTemp("", "zsp-release-relay-*")
	if err != nil {
		return "", "", "", stop, err
	}
	relayAddr, err := freeAddress()
	if err != nil {
		os.RemoveAll(dir)
		return "", "", "", stop, err
	}
	blossomAddr, err := freeAddress()
	if err != nil {
		os.RemoveAll(dir)
		return "", "", "", stop, err
	}
	analyticsAddr, err := freeAddress()
	if err != nil {
		os.RemoveAll(dir)
		return "", "", "", stop, err
	}
	dashboardAddr, err := freeAddress()
	if err != nil {
		os.RemoveAll(dir)
		return "", "", "", stop, err
	}

	var logs strings.Builder
	relayLogs = &logs
	cmd := exec.Command(binary, "run")
	cmd.Dir = dir
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	cmd.Env = mergeEnv(os.Environ(), []string{
		"SYSTEM_DIRECTORY_PATH=" + dir,
		"SYSTEM_LOG_LEVEL=info",
		"RELAY_HOSTNAME=127.0.0.1",
		"RELAY_ADDRESS=" + relayAddr,
		"RELAY_NAME=Zapstore release test",
		"RELAY_PUBKEY=" + fixturePubkey,
		"RELAY_DESCRIPTION=Release test fixture",
		"RELAY_URL=ws://" + relayAddr,
		"RELAY_ICON=https://example.invalid/icon.png",
		"RELAY_BANNER=https://example.invalid/banner.png",
		"RELAY_CONTACT=" + fixturePubkey,
		"RELAY_SOFTWARE=https://github.com/zapstore/relay",
		"SIGN_WITH=1",
		"DELTA_INTERVAL=1h",
		"ANALYTICS_GEO_ENABLED=false",
		"ANALYTICS_API_ADDRESS=" + analyticsAddr,
		"BLOSSOM_HOSTNAME=127.0.0.1",
		"BLOSSOM_ADDRESS=" + blossomAddr,
		"DASHBOARD_HOSTNAME=127.0.0.1",
		"DASHBOARD_ADDRESS=" + dashboardAddr,
		"DASHBOARD_ADMIN_PUBKEYS=" + fixturePubkey,
		"RATE_INITIAL_TOKENS=100000",
		"RATE_MAX_TOKENS=100000",
		"RATE_TOKENS_PER_INTERVAL=100000",
	})
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return "", "", "", stop, fmt.Errorf("start relay: %w", err)
	}
	stop = func() {
		stopRelay(cmd, &logs)
		os.RemoveAll(dir)
	}
	if err := waitTCP(relayAddr, &logs); err != nil {
		stop()
		return "", "", "", func() {}, err
	}
	if err := waitTCP(blossomAddr, &logs); err != nil {
		stop()
		return "", "", "", func() {}, err
	}
	socketPath = filepath.Join(dir, "data", "relay.sock")
	if err := waitUnix(socketPath, &logs); err != nil {
		stop()
		return "", "", "", func() {}, err
	}
	return "ws://" + relayAddr, "http://" + blossomAddr, socketPath, stop, nil
}

func buildRelay(root, out string) error {
	cmd := exec.Command("go", "build", "-buildvcs=false", "-tags", "fts5", "-o", out, "./cmd")
	cmd.Dir = filepath.Join(root, "relay")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if runtime.GOOS == "darwin" {
		cmd.Env = append(cmd.Env, "CGO_LDFLAGS=-Wl,-no_warn_duplicate_libraries")
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build relay: %w", err)
	}
	return nil
}

func workspaceRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		zsp := filepath.Join(dir, "zsp", "go.mod")
		relay := filepath.Join(dir, "relay", "cmd")
		if _, zspErr := os.Stat(zsp); zspErr == nil {
			if _, relayErr := os.Stat(relay); relayErr == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("zsp/ and relay/ not found from %s", wd)
		}
		dir = parent
	}
}

func freeAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return address, nil
}

func waitTCP(address string, logs *strings.Builder) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	dialer := net.Dialer{Timeout: 50 * time.Millisecond}
	for {
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("relay did not listen on %s\n%s", address, logs.String())
		case <-ticker.C:
		}
	}
}

func waitUnix(path string, logs *strings.Builder) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	dialer := net.Dialer{Timeout: 50 * time.Millisecond}
	for {
		conn, err := dialer.DialContext(ctx, "unix", path)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("relay socket %s did not open\n%s", path, logs.String())
		case <-ticker.C:
		}
	}
}

func stopRelay(cmd *exec.Cmd, logs *strings.Builder) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "signal: interrupt") {
			fmt.Fprintf(os.Stderr, "stop relay: %v\n%s", err, logs.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		fmt.Fprintf(os.Stderr, "relay did not stop\n%s", logs.String())
	}
}

func mergeEnv(base, extra []string) []string {
	keys := make(map[string]string, len(base)+len(extra))
	for _, item := range append(base, extra...) {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		keys[key] = value
	}
	delete(keys, "DEFENDER_URL")
	for key := range keys {
		if strings.HasPrefix(key, "BUNNY_") {
			delete(keys, key)
		}
	}
	out := make([]string, 0, len(keys))
	for key, value := range keys {
		out = append(out, key+"="+value)
	}
	return out
}
