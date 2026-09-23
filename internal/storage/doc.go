// Package storage persists Conversations in SQLite on the local machine,
// encrypting message bodies, Display Names and pinned keys at rest. Nothing
// outside this package sees the encryption.
package storage
