package hotbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func serveOnce(t *testing.T, status byte, resp []byte, gotBody *[]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "h.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		op, body, err := readFrame(conn)
		if err != nil || op != opProof {
			return
		}
		*gotBody = body
		_ = writeFrame(conn, status, resp)
	}()
	return path
}

func TestSignProof(t *testing.T) {
	pubkey := bytes.Repeat([]byte{0xab}, 32)
	certHash := bytes.Repeat([]byte{0xcd}, 32)
	sig := []byte{1, 2, 3}
	resp := append(append(append([]byte{}, pubkey...), certHash...), 0, 0, 0, 3)
	resp = append(resp, sig...)
	var got []byte
	path := serveOnce(t, statusOK, resp, &got)
	pin := strings.Repeat("p", 32)

	proof, err := Client{Socket: path, PIN: pin}.SignProof(t.Context(), 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if proof.PublicKey != strings.Repeat("ab", 32) || proof.CertificateHash != strings.Repeat("cd", 32) || !bytes.Equal(proof.Signature, sig) {
		t.Fatalf("proof = %+v", proof)
	}
	want := binary.BigEndian.AppendUint16(nil, 32)
	want = append(want, pin...)
	want = binary.BigEndian.AppendUint64(want, 100)
	want = binary.BigEndian.AppendUint64(want, 200)
	if !bytes.Equal(got, want) {
		t.Fatalf("request body = %x", got)
	}
}

func TestSignProofStatus(t *testing.T) {
	var got []byte
	path := serveOnce(t, statusPIN, nil, &got)
	_, err := Client{Socket: path, PIN: strings.Repeat("p", 32)}.SignProof(t.Context(), 1, 2)
	if err == nil || !strings.Contains(err.Error(), "HOTBOX_PIN") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("p", 32)) {
		t.Fatal("error contains the PIN")
	}
}

func TestSignProofKeepsDialError(t *testing.T) {
	_, err := Client{Socket: filepath.Join(t.TempDir(), "missing.sock"), PIN: strings.Repeat("p", 32)}.SignProof(t.Context(), 1, 2)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want it to wrap fs.ErrNotExist", err)
	}
}

func TestSignProofCancellationUnblocksRead(t *testing.T) {
	// macOS rejects Unix socket paths longer than 104 bytes, and t.TempDir is longer here.
	dir, err := os.MkdirTemp("/tmp", "hb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-t.Context().Done()
	}()

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() {
		_, err := Client{Socket: path, PIN: strings.Repeat("p", 32)}.SignProof(ctx, 1, 2)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SignProof did not return after cancellation")
	}
}
