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

	t.Run("check missing APK envelope", func(t *testing.T) {
		result := runZSP(t, context.Background(), "check", filepath.Join(t.TempDir(), "missing.apk"))
		requireErrorEnvelope(t, result, 1, "check", "invalid_arguments")
	})

	t.Run("utils extract-apk missing APK envelope", func(t *testing.T) {
		result := runZSP(t, context.Background(), "utils", "extract-apk", filepath.Join(t.TempDir(), "missing.apk"))
		requireErrorEnvelope(t, result, 1, "utils", "invalid_apk")
	})

	t.Run("utils unknown operation", func(t *testing.T) {
		result := runZSP(t, context.Background(), "utils", "unknown", "app.apk")
		requireErrorEnvelope(t, result, 1, "utils", "invalid_arguments")
	})

	t.Run("skill prints the agent skill", func(t *testing.T) {
		result := runZSP(t, context.Background(), "skill")
		if result.exitCode != 0 || !strings.HasPrefix(result.stdout, "---\nname: zapstore-publish") {
			t.Fatalf("skill exit=%d stdout=%.80q stderr=%q", result.exitCode, result.stdout, result.stderr)
		}
	})

	t.Run("check no result envelope", func(t *testing.T) {
		dir := t.TempDir()
		writeZapstoreYAML(t, dir)
		result := runZSPDir(t, context.Background(), dir, "check", "-s", t.TempDir())
		requireErrorEnvelope(t, result, 1, "check", "source_failed")
	})

	t.Run("apk argument rejected", func(t *testing.T) {
		result := runZSP(t, context.Background(), "publish", "app.apk")
		requireErrorEnvelope(t, result, 1, "publish", "invalid_arguments")
		if !strings.Contains(result.stderr, "release_source") {
			t.Fatalf("stderr = %q, want release_source", result.stderr)
		}
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

		dir := t.TempDir()
		writeZapstoreYAML(t, dir)
		command := exec.Command(zspBinary, "check", "-s", serverURL+"/release.apk")
		command.Dir = dir
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

func writeZapstoreYAML(t *testing.T, dir string) {
	t.Helper()
	body := "repository: https://github.com/example/app\n"
	if err := os.WriteFile(filepath.Join(dir, "zapstore.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runZSP(t *testing.T, ctx context.Context, arguments ...string) commandResult {
	t.Helper()
	return runZSPDir(t, ctx, t.TempDir(), arguments...)
}

func runZSPDir(t *testing.T, ctx context.Context, dir string, arguments ...string) commandResult {
	t.Helper()
	command := exec.CommandContext(ctx, zspBinary, arguments...)
	command.Dir = dir
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
