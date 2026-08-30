package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// Random creates opaque identifiers from cryptographically secure random bytes.
type Random struct {
	Reader io.Reader
}

// NewQueryID implements ports.QueryIDGenerator.
func (g Random) NewQueryID() (string, error) {
	return g.New("qry")
}

// New creates an opaque identifier with a human-readable type prefix.
func (g Random) New(prefix string) (string, error) {
	reader := g.Reader
	if reader == nil {
		reader = rand.Reader
	}
	buffer := make([]byte, 16)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return "", fmt.Errorf("read random identifier bytes: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(buffer), nil
}
