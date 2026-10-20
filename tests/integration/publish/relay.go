//go:build publish && !race

package publish

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/zapstore/zsp/tests/integration/fixture"
)

const fixturePubkey = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

func startLocalRelay() (relayURL, blossomURL string, stop func(), err error) {
	stop = func() {}
	binary, cleanup, err := fixture.Relay()
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
		"DATA_DIR=" + dir,
		"LOG_LEVEL=info",
		"RELAY_HOSTNAME=127.0.0.1",
		"RELAY_ADDRESS=" + relayAddr,
		"RELAY_NAME=Zapstore local publish",
		"RELAY_PUBKEY=" + fixturePubkey,
		"RELAY_DESCRIPTION=Local publish fixture",
		"RELAY_URL=ws://" + relayAddr,
		"RELAY_ICON=https://example.invalid/icon.png",
		"RELAY_BANNER=https://example.invalid/banner.png",
		"RELAY_CONTACT=" + fixturePubkey,
		"RELAY_SOFTWARE=https://github.com/zapstore/relay",
		"SIGN_WITH=1",
		"DELTA_INTERVAL=1h",
		"ANALYTICS_ADDRESS=" + analyticsAddr,
		"BLOSSOM_HOSTNAME=127.0.0.1",
		"BLOSSOM_ADDRESS=" + blossomAddr,
		"DASHBOARD_HOSTNAME=127.0.0.1",
		"DASHBOARD_ADDRESS=" + dashboardAddr,
		"ADMIN_PUBKEYS=" + fixturePubkey,
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
		if strings.HasPrefix(key, "BUNNY_") || strings.HasPrefix(key, "BLOSSOM_BUNNY_") {
			delete(keys, key)
		}
	}
	out := make([]string, 0, len(keys))
	for key, value := range keys {
		out = append(out, key+"="+value)
	}
	return out
}
