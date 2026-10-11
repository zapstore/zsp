package hotbox

import (
	"bytes"
	"encoding/binary"
	"net"
	"path/filepath"
	"strings"
	"testing"
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
