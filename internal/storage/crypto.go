package storage

import (
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// boxVersion is the leading byte of every encrypted field, so the format can
// change one column at a time without a migration.
const boxVersion = 0x01

// errUnreadable marks a field that would not decrypt — the wrong db_key, or
// a database someone has edited. Every path that hits it fails loudly; a
// pin or a message that cannot be read must never pass for absent.
var errUnreadable = errors.New("storage: the database cannot be read with this db_key")

// ad binds a ciphertext to the exact cell that holds it, so that shuffling
// encrypted values between rows or columns — repointing a pinned key at a
// different Conversation, say — fails to decrypt instead of succeeding
// quietly.
func ad(table, column string, rowid int64) []byte {
	return fmt.Appendf(nil, "%s.%s:%d", table, column, rowid)
}

// seal encrypts one field for one cell: boxVersion, then the random nonce,
// then the XChaCha20-Poly1305 ciphertext.
func seal(aead cipher.AEAD, table, column string, rowid int64, plaintext []byte) ([]byte, error) {
	box := make([]byte, 1+aead.NonceSize(), 1+aead.NonceSize()+len(plaintext)+aead.Overhead())
	box[0] = boxVersion
	if _, err := rand.Read(box[1 : 1+aead.NonceSize()]); err != nil {
		return nil, fmt.Errorf("storage: reading randomness: %w", err)
	}
	return aead.Seal(box, box[1:1+aead.NonceSize()], plaintext, ad(table, column, rowid)), nil
}

// unseal decrypts one field from one cell.
func unseal(aead cipher.AEAD, table, column string, rowid int64, box []byte) ([]byte, error) {
	if len(box) < 1+aead.NonceSize() || box[0] != boxVersion {
		return nil, errUnreadable
	}
	plaintext, err := aead.Open(nil, box[1:1+aead.NonceSize()], box[1+aead.NonceSize():], ad(table, column, rowid))
	if err != nil {
		return nil, fmt.Errorf("%w: %s.%s", errUnreadable, table, column)
	}
	return plaintext, nil
}

// newAEAD builds the XChaCha20-Poly1305 cipher every field goes through.
func newAEAD(key []byte) (cipher.AEAD, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("storage: building the cipher: %w", err)
	}
	return aead, nil
}
