package nostr

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsRateLimited(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nip01 prefix", relayRejection(errors.New("msg: rate-limited: slow down")), true},
		{"wrapped", newOpError("publish", relayRejection(errors.New("msg: rate-limited: slow down"))), true},
		{"other rejection", relayRejection(errors.New("msg: blocked: not allowed")), false},
		{"transport text", fmt.Errorf("rate-limited: not a relay reply"), false},
		{"cancelled", relayRejection(context.Canceled), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRateLimited(tt.err); got != tt.want {
				t.Errorf("IsRateLimited(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
