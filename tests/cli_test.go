// Package tests verifies compiled ZSP commands through their public process
// interfaces. It imports no ZSP packages and does not start a relay.
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	zspBinary      string
	buildDirectory string
)

func TestMain(m *testing.M) {
	var err error
	buildDirectory, err = os.MkdirTemp("", "zsp-cli-binaries-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create CLI build directory:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(buildDirectory)

	root := moduleRoot()
	zspBinary = filepath.Join(buildDirectory, "zsp")
	command := exec.Command("go", "build", "-trimpath", "-o", zspBinary, "./cmd/zsp")
	command.Dir = root
	if result, buildErr := command.CombinedOutput(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "build ./cmd/zsp: %v\n%s", buildErr, result)
		os.Exit(1)
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
}

type commandResult struct {
	stdout   string
	stderr   string
	exitCode int
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
		panic("locate CLI test file")
	}
	return filepath.Dir(filepath.Dir(file))
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
