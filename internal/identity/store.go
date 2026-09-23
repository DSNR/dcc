package identity

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FileName is the file within the application directory that holds the
// Identity. Backing dcc up is copying it; there is nothing else to save.
const FileName = "identity.json"

// dirMode and fileMode keep both the Identity key and the database key to the
// account that owns them. They are the MVP's only protection for either.
const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

// fileVersion is the on-disk format's version. It is deliberately separate
// from the protocol version: the file is local and can change shape without
// anything on the wire moving.
const fileVersion = 1

// storedIdentity is identity.json. The public key is not stored — it is
// derived from the private half on load, so the file cannot disagree with
// itself.
type storedIdentity struct {
	V           int    `json:"v"`
	IdentityKey string `json:"identity_key"`
	DBKey       string `json:"db_key"`
}

// Reset reports that a damaged identity.json was set aside and a fresh
// Identity generated in its place. Load returns it alongside a perfectly
// usable Identity, because the consequence is not technical but social: the
// participant's Peers will see a changed Security Code and must verify them
// again. Callers must show Warning; nothing about this may pass in silence.
type Reset struct {
	// SetAside is the path the damaged file was moved to, kept verbatim in
	// case its owner can recover the old Identity from it by hand.
	SetAside string
	// Cause is what was wrong with the file.
	Cause error
}

// Warning is the message to put in front of the participant.
func (r *Reset) Warning() string {
	return fmt.Sprintf("Your dcc Identity file was damaged (%v). It has been set aside as %s "+
		"and a new Identity generated — anyone who has verified you before will see a changed "+
		"Security Code and must verify you again.", r.Cause, r.SetAside)
}

// Dir reports the application directory, os.UserConfigDir()/dcc. All app data
// lives there; the caller creates nothing, Load does that.
func Dir() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("identity: locating the user config directory: %w", err)
	}
	return filepath.Join(config, "dcc"), nil
}

// Load returns the Identity stored in dir, creating dir and a fresh Identity
// on first run.
//
// A damaged file is never continued with and never quietly dropped: it is
// renamed aside, a new Identity replaces it, and the non-nil *Reset says so.
// A file that merely cannot be read — a permission problem, a half-mounted
// home directory — is an error instead, since regenerating there would throw
// away an Identity that is still intact.
func Load(dir string) (Identity, *Reset, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return Identity{}, nil, fmt.Errorf("identity: creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, FileName)

	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		id, err := create(path)
		return id, nil, err
	case err != nil:
		return Identity{}, nil, fmt.Errorf("identity: reading %s: %w", path, err)
	}

	id, parseErr := parse(b)
	if parseErr == nil {
		return id, nil, nil
	}

	aside, err := setAside(path)
	if err != nil {
		return Identity{}, nil, err
	}
	id, err = create(path)
	if err != nil {
		return Identity{}, nil, err
	}
	return id, &Reset{SetAside: aside, Cause: parseErr}, nil
}

// parse reads a stored Identity. Every failure means the same thing to the
// caller — this file cannot be used — so the errors describe the damage for
// the warning rather than being distinguished by type.
func parse(b []byte) (Identity, error) {
	var stored storedIdentity
	if err := json.Unmarshal(b, &stored); err != nil {
		return Identity{}, fmt.Errorf("it is not valid JSON: %w", err)
	}
	if stored.V != fileVersion {
		return Identity{}, fmt.Errorf("it is version %d, and this build reads version %d", stored.V, fileVersion)
	}
	private, err := decodeKey("identity_key", stored.IdentityKey)
	if err != nil {
		return Identity{}, err
	}
	dbKey, err := decodeKey("db_key", stored.DBKey)
	if err != nil {
		return Identity{}, err
	}
	public, err := publicFor(private)
	if err != nil {
		return Identity{}, fmt.Errorf("its %w", errUnusableKey)
	}
	return Identity{private: private, public: public, dbKey: dbKey}, nil
}

// decodeKey reads one base64 key field at its exact length.
func decodeKey(field, value string) ([KeySize]byte, error) {
	var key [KeySize]byte
	if value == "" {
		return key, fmt.Errorf("it has no %s", field)
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return key, fmt.Errorf("its %s is not base64: %w", field, err)
	}
	if len(raw) != KeySize {
		return key, fmt.Errorf("its %s is %d bytes, want %d", field, len(raw), KeySize)
	}
	copy(key[:], raw)
	return key, nil
}

// create generates an Identity and writes it to path.
func create(path string) (Identity, error) {
	id, err := generate()
	if err != nil {
		return Identity{}, err
	}
	stored := storedIdentity{
		V:           fileVersion,
		IdentityKey: base64.StdEncoding.EncodeToString(id.private[:]),
		DBKey:       base64.StdEncoding.EncodeToString(id.dbKey[:]),
	}
	// Indented, because a participant looking after their own backup should
	// be able to read the one file the whole install depends on.
	b, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return Identity{}, fmt.Errorf("identity: encoding %s: %w", path, err)
	}
	if err := writeFile(path, append(b, '\n')); err != nil {
		return Identity{}, err
	}
	return id, nil
}

// writeFile writes through a temporary file in the same directory and renames
// it into place, so that an interrupted write leaves the old file rather than
// a half-written Identity. The temporary carries the final mode from birth:
// the keys are never on disk world-readable, not even briefly.
func writeFile(path string, b []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, FileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("identity: creating a temporary file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("identity: securing %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("identity: writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("identity: flushing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("identity: closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("identity: writing %s: %w", path, err)
	}
	return nil
}

// setAside renames a damaged file out of the way, preserving it byte for
// byte. The name is timestamped so that repeated damage never overwrites an
// earlier casualty — each one may still hold a recoverable Identity.
func setAside(path string) (string, error) {
	base := fmt.Sprintf("%s.corrupt-%s", path, time.Now().UTC().Format("20060102T150405Z"))
	aside := base
	for n := 2; ; n++ {
		if _, err := os.Lstat(aside); errors.Is(err, fs.ErrNotExist) {
			break
		}
		aside = fmt.Sprintf("%s-%d", base, n)
	}
	if err := os.Rename(path, aside); err != nil {
		return "", fmt.Errorf("identity: setting aside the damaged %s: %w", path, err)
	}
	return aside, nil
}
