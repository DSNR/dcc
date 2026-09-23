// Package identity holds a participant's persistent local keypair and
// derives the Security Code from a pair of Identities, so that the code is
// the same every Session between the same two people.
//
// One file — identity.json, mode 0600, in the application directory — holds
// everything that makes an install recognisable: the X25519 Identity key the
// Noise handshake authenticates with, and the separate random key the
// storage package encrypts the database under. Copying that one file to
// another machine moves the install; losing it means a new Identity and a
// changed Security Code, which is why a damaged file is set aside rather than
// deleted and the caller is handed a warning it must show.
//
// There is no passphrase and no keychain in the MVP: the file's protection is
// the file's mode and the account it sits under.
package identity
