//go:build publish && !race

package publish

import (
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

func startLocalRelay() (relayURL, blossomURL string, stop func(), err error) {
	stop = func() {}
	root, err := workspaceRoot()
	if err != nil {
		return "", "", stop, err
	}
	binary, cleanup, err := relayBinary(root)
	if err != nil {
		return "", "", stop, err
	}

	dir, err := os.MkdirTemp("", "zsp-publish-relay-*")
	if err != nil {
		cleanup()
		return "", "", stop, err
	}

	relayAddr, err := freeAddress()
	if err != nil {
		cleanup()
		os.RemoveAll(dir)
		return "", "", stop, err
	}
	blossomAddr, err := freeAddress()
	if err != nil {
		cleanup()
		os.RemoveAll(dir)
		return "", "", stop, err
	}
	analyticsAddr, err := freeAddress()
	if err != nil {
		cleanup()
		os.RemoveAll(dir)
		return "", "", stop, err
	}
	dashboardAddr, err := freeAddress()
	if err != nil {
		cleanup()
		os.RemoveAll(dir)
		return "", "", stop, err
	}

	var logs strings.Builder
	cmd := exec.Command(binary, "run")
	cmd.Dir = dir
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	cmd.Env = mergeEnv(os.Environ(), []string{
		"SYSTEM_DIRECTORY_PATH=" + dir,
		"SYSTEM_LOG_LEVEL=info",
		"RELAY_HOSTNAME=127.0.0.1",
		"RELAY_ADDRESS=" + relayAddr,
		"RELAY_NAME=Zapstore local publish",
		"RELAY_PUBKEY=" + fixturePubkey,
		"RELAY_DESCRIPTION=Local publish fixture",
		"RELAY_URL=ws://" + relayAddr,
		"CATALOG_RELAYS=ws://" + relayAddr,
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
		cleanup()
		os.RemoveAll(dir)
		return "", "", stop, fmt.Errorf("start relay: %w", err)
	}
	stop = func() {
		stopRelay(cmd, &logs)
		os.RemoveAll(dir)
		cleanup()
	}
	if err := waitTCP(relayAddr, &logs); err != nil {
		stop()
		return "", "", func() {}, err
	}
	if err := waitTCP(blossomAddr, &logs); err != nil {
		stop()
		return "", "", func() {}, err
	}
	return "ws://" + relayAddr, "http://" + blossomAddr, stop, nil
}

func relayBinary(root string) (string, func(), error) {
	noop := func() {}
	if binary := strings.TrimSpace(os.Getenv("RELAY_BINARY")); binary != "" {
		return binary, noop, nil
	}
	dir := strings.TrimSpace(os.Getenv("RELAY_DIR"))
	if dir == "" {
		dir = filepath.Join(root, "relay")
	}
	tmp, err := os.MkdirTemp("", "zsp-publish-relay-bin-*")
	if err != nil {
		return "", nil, err
	}
	out := filepath.Join(tmp, "relay")
	cmd := exec.Command("go", "build", "-tags", "fts5", "-o", out, "./cmd")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if runtime.GOOS == "darwin" {
		cmd.Env = append(cmd.Env, "CGO_LDFLAGS=-Wl,-no_warn_duplicate_libraries")
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(tmp)
		return "", nil, fmt.Errorf("build relay: %w", err)
	}
	return out, func() { os.RemoveAll(tmp) }, nil
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
			if _, relayErr := os.Stat(relay); relayErr == nil || strings.TrimSpace(os.Getenv("RELAY_BINARY")) != "" {
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
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("relay did not listen on %s\n%s", address, logs.String())
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
