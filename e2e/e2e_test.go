// Package e2e verifies the compiled ZSP commands through their public process
// interfaces. It intentionally imports no ZSP packages.
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

var (
	zspBinary       string
	testRelayBinary string
	buildDirectory  string
)

func TestMain(m *testing.M) {
	var err error
	buildDirectory, err = os.MkdirTemp("", "zsp-e2e-binaries-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create E2E build directory:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(buildDirectory)

	root := moduleRoot()
	zspBinary = filepath.Join(buildDirectory, "zsp")
	testRelayBinary = filepath.Join(buildDirectory, "test-relay")
	for output, pkg := range map[string]string{
		zspBinary:       "./cmd/zsp",
		testRelayBinary: "./cmd/test-relay",
	} {
		command := exec.Command("go", "build", "-trimpath", "-o", output, pkg)
		command.Dir = root
		if result, buildErr := command.CombinedOutput(); buildErr != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, buildErr, result)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func TestCLI(t *testing.T) {
	t.Run("version", func(t *testing.T) {
		result := runZSP(t, context.Background(), "--version")
		if result.exitCode != 0 || strings.TrimSpace(result.stdout) == "" {
			t.Fatalf("version exit=%d stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
		}
	})

	t.Run("extract invalid APK envelope", func(t *testing.T) {
		result := runZSP(t, context.Background(), "utils", "extract-apk", filepath.Join(t.TempDir(), "missing.apk"))
		requireErrorEnvelope(t, result, 1, "extract_apk", "invalid_apk")
	})

	t.Run("check no result envelope", func(t *testing.T) {
		result := runZSP(t, context.Background(), "publish", "--check", "--skip-metadata", "-s", t.TempDir())
		requireErrorEnvelope(t, result, 1, "check", "source_failed")
	})

	t.Run("unknown command envelope", func(t *testing.T) {
		result := runZSP(t, context.Background(), "not-a-command")
		requireErrorEnvelope(t, result, 1, "not-a-command", "invalid_arguments")
	})

	t.Run("interrupt yields cancellation envelope", func(t *testing.T) {
		requestStarted := make(chan struct{})
		serverURL := startHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
			close(requestStarted)
			<-r.Context().Done()
		})

		command := exec.Command(zspBinary, "publish", "--check", "--skip-metadata", "-s", serverURL+"/release.apk")
		command.Dir = t.TempDir()
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-requestStarted:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			t.Fatal("CLI did not request the release source")
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			_ = command.Process.Kill()
			t.Fatalf("interrupt CLI: %v", err)
		}
		err := command.Wait()
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 130 {
			t.Fatalf("interrupt result=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
		requireErrorEnvelope(t, commandResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitError.ExitCode()}, 130, "check", "cancelled")
	})

	t.Run("test relay child starts", func(t *testing.T) {
		relayAddress := unusedAddress(t)
		blossomAddress := unusedAddress(t)
		command := exec.Command(testRelayBinary)
		command.Dir = moduleRoot()
		command.Env = append(os.Environ(),
			"RELAY_ADDRESS="+relayAddress,
			"RELAY_BLOSSOM_ADDRESS="+blossomAddress,
			"RELAY_BLOSSOM_HOSTNAME=127.0.0.1",
			"CATALOG_FIXTURE_DIR="+t.TempDir(),
			"CATALOG_SOURCE_DB="+filepath.Join(t.TempDir(), "missing.db"),
		)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := command.Process.Signal(os.Interrupt); err != nil {
				t.Errorf("interrupt test relay: %v", err)
				return
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("stop test relay: %v; stderr=%s", err, stderr.String())
				}
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				<-done
				t.Errorf("test relay did not stop; stderr=%s", stderr.String())
			}
		})
		waitForHTTP(t, "http://"+relayAddress+"/_test/state")
	})
}

func TestWizard(t *testing.T) {
	command := exec.Command(zspBinary)
	command.Dir = t.TempDir()
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	var output lockedBuffer
	readDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&output, terminal)
		close(readDone)
	}()
	t.Cleanup(func() {
		_ = terminal.Close()
		<-readDone
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !strings.Contains(output.String(), "App source") {
		select {
		case <-deadline.C:
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("wizard did not show its first prompt: %q", output.String())
		case <-ticker.C:
		}
	}
	if _, err := terminal.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 130 {
		t.Fatalf("wizard interrupt result=%v output=%q", err, output.String())
	}
}

type commandResult struct {
	stdout   string
	stderr   string
	exitCode int
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.b.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.b.String()
}

func runZSP(t *testing.T, ctx context.Context, arguments ...string) commandResult {
	t.Helper()
	command := exec.CommandContext(ctx, zspBinary, arguments...)
	command.Dir = t.TempDir()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := commandResult{stdout: stdout.String(), stderr: stderr.String()}
	if err == nil {
		return result
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.exitCode = exitError.ExitCode()
		return result
	}
	t.Fatalf("run zsp: %v", err)
	return commandResult{}
}

func requireErrorEnvelope(t *testing.T, result commandResult, wantExit int, wantOperation, wantCode string) {
	t.Helper()
	if result.exitCode != wantExit {
		t.Fatalf("exit=%d want=%d stdout=%q stderr=%q", result.exitCode, wantExit, result.stdout, result.stderr)
	}
	if result.stdout != "" {
		t.Fatalf("error output appeared on stdout: %q", result.stdout)
	}
	var envelope struct {
		OK        bool   `json:"ok"`
		Operation string `json:"operation"`
		Error     struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.stderr), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v; stderr=%q", err, result.stderr)
	}
	if envelope.OK || envelope.Operation != wantOperation || envelope.Error.Code != wantCode {
		t.Fatalf("envelope=%s want ok=false operation=%q code=%q", result.stderr, wantOperation, wantCode)
	}
}

func moduleRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("locate E2E test file")
	}
	return filepath.Dir(filepath.Dir(file))
}

func unusedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func waitForHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service did not become ready: %s", url)
}

func startHTTPServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})
	return "http://" + listener.Addr().String()
}
