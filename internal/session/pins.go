package session

import (
	"sync"

	"github.com/DSNR/dcc/internal/identity"
)

// Pins remembers which Identity each Display Name was last accepted with,
// which is what turns a returning Peer's new key into the "Security Code
// changed" warning instead of a fresh first meeting. The storage package
// implements it on the Conversation table; MemoryPins stands in for tests
// and for a Session that keeps no history.
type Pins interface {
	// Pinned reports the Identity name was last accepted with.
	Pinned(name string) (identity.PublicKey, bool)
	// Pin records that name was accepted with key, replacing any earlier
	// pin. An error means the acceptance was not recorded, and the caller
	// must not proceed as if it had been.
	Pin(name string, key identity.PublicKey) error
}

// MemoryPins is a Pins that lives and dies with the process.
type MemoryPins struct {
	mu   sync.Mutex
	pins map[string]identity.PublicKey
}

// NewMemoryPins returns an empty in-memory pin store.
func NewMemoryPins() *MemoryPins {
	return &MemoryPins{pins: make(map[string]identity.PublicKey)}
}

// Pinned implements Pins.
func (p *MemoryPins) Pinned(name string) (identity.PublicKey, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key, ok := p.pins[name]
	return key, ok
}

// Pin implements Pins.
func (p *MemoryPins) Pin(name string, key identity.PublicKey) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pins[name] = key
	return nil
}
