// Package hotbox asks a running hotbox for NIP-C1 certificate proof signatures
// over its same-user Unix socket. The certificate key never leaves hotbox.
package hotbox

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	protoVersion = 1
	opProof      = 3

	statusOK        = 0
	statusPIN       = 1
	statusMechanism = 2
	statusBad       = 3

	maxFrame = 1 << 20
	timeout  = 30 * time.Second
)

// Proof is the certificate signature hotbox returns for its own npub.
type Proof struct {
	PublicKey       string
	CertificateHash string
	Signature       []byte
}

// Client signs C1 proofs through one hotbox socket.
type Client struct {
	Socket string
	PIN    string
}

// SignProof asks hotbox to sign the C1 proof message for createdAt and expiry.
func (c Client) SignProof(ctx context.Context, createdAt, expiry int64) (Proof, error) {
	if len(c.PIN) != 32 {
		return Proof{}, errors.New("HOTBOX_PIN is invalid; paste the block hotbox prints at startup")
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return Proof{}, fmt.Errorf("hotbox is not listening on %s; start it and unlock the vault: %w", c.Socket, err)
	}
	defer conn.Close()
	// Closing the connection unblocks reads when ctx ends without a deadline.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	body := make([]byte, 0, 2+len(c.PIN)+16)
	body = binary.BigEndian.AppendUint16(body, uint16(len(c.PIN)))
	body = append(body, c.PIN...)
	body = binary.BigEndian.AppendUint64(body, uint64(createdAt))
	body = binary.BigEndian.AppendUint64(body, uint64(expiry))
	if err := writeFrame(conn, opProof, body); err != nil {
		return Proof{}, fmt.Errorf("hotbox request: %w", connErr(ctx, err))
	}
	status, resp, err := readFrame(conn)
	if err != nil {
		return Proof{}, fmt.Errorf("hotbox response: %w", connErr(ctx, err))
	}
	switch status {
	case statusOK:
	case statusPIN:
		return Proof{}, errors.New("hotbox rejected HOTBOX_PIN")
	case statusMechanism:
		return Proof{}, errors.New("hotbox cannot sign a C1 proof with this key type")
	case statusBad:
		return Proof{}, errors.New("hotbox refused the C1 proof request; rebuild and restart hotbox if it predates C1 proof support, and check the system clock")
	default:
		return Proof{}, errors.New("hotbox could not sign the C1 proof; its log has the reason")
	}
	if len(resp) < 68 {
		return Proof{}, errors.New("hotbox proof response is short")
	}
	n := binary.BigEndian.Uint32(resp[64:68])
	if uint32(len(resp)-68) != n || n == 0 {
		return Proof{}, errors.New("hotbox proof response is malformed")
	}
	return Proof{
		PublicKey:       hex.EncodeToString(resp[:32]),
		CertificateHash: hex.EncodeToString(resp[32:64]),
		Signature:       append([]byte(nil), resp[68:]...),
	}, nil
}

// connErr prefers the context error when ctx closed the connection.
func connErr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func writeFrame(w io.Writer, kind byte, body []byte) error {
	var hdr [6]byte
	hdr[0] = protoVersion
	hdr[1] = kind
	binary.BigEndian.PutUint32(hdr[2:], uint32(len(body)))
	if _, err := w.Write(append(hdr[:], body...)); err != nil {
		return err
	}
	return nil
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [6]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	if hdr[0] != protoVersion {
		return 0, nil, errors.New("unsupported frame version")
	}
	n := binary.BigEndian.Uint32(hdr[2:])
	if n > maxFrame {
		return 0, nil, errors.New("frame is too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return hdr[1], body, nil
}
