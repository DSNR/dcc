package storage

import (
	"crypto/cipher"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/session"
)

// FileName is the database file within the application directory. Together
// with identity.FileName it is the whole of a dcc install: copying the two
// files moves both recognition and history.
const FileName = "conversations.db"

// fileMode keeps the database to the account that owns it, like the Identity
// file — defence in depth behind the encrypted fields.
const fileMode os.FileMode = 0o600

// schemaVersion is the database layout's version, kept in SQLite's
// user_version. Like the Identity file's, it is local and moves
// independently of the protocol.
const schemaVersion = 1

// schema is the whole layout. A Conversation is one row per Peer, keyed by
// their pinned Identity — not by who Hosted — with the pinned key and the
// Display Name encrypted on the row. Messages carry their wire id so a
// resend after a reconnect can be recognised, and their body encrypted.
const schema = `
CREATE TABLE IF NOT EXISTS conversations (
	id INTEGER PRIMARY KEY,
	peer_key BLOB NOT NULL,
	name BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY,
	conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	msg_id TEXT NOT NULL UNIQUE,
	mine INTEGER NOT NULL,
	body BLOB NOT NULL,
	status INTEGER NOT NULL,
	at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_by_conversation ON messages(conversation_id, id);
`

// Conversation is one Peer's history as the UIs list it: who, how much, and
// how recent. Peer is the currently pinned Identity — after an accepted
// Identity change it is the new key, over the same history.
type Conversation struct {
	Peer identity.PublicKey
	Name string
	// Messages is how many are kept; LastAt is when the newest was.
	Messages int
	LastAt   time.Time
}

// Message is one kept message, decrypted, in the order it was kept.
type Message struct {
	ID   string
	Mine bool
	Body string
	At   time.Time
	// Status is where delivery stood when last recorded. A message still
	// TextSent after a restart simply never had its delivery confirmed.
	Status session.DeliveryStatus
}

// Store is the local history: SQLite in the application directory, with
// every body, Display Name and pinned key encrypted under the db_key before
// it touches the file. It implements session.Pins and session.Log, and is
// safe for the Session and the UI to use at once.
type Store struct {
	mu   sync.Mutex
	db   *sql.DB
	aead cipher.AEAD
}

// Interface checks: the Session pins and keeps history through exactly this
// Store.
var (
	_ session.Pins    = (*Store)(nil)
	_ session.History = (*Store)(nil)
)

// Open opens or creates the database at path, encrypting under key. A
// database written under a different key is refused here, whole — a pin or
// a message that cannot be read must never pass for absent later.
func Open(path string, key [identity.KeySize]byte) (*Store, error) {
	aead, err := newAEAD(key[:])
	if err != nil {
		return nil, err
	}
	// One connection, so writes from the Session and reads from the UI
	// queue behind each other instead of learning about SQLite's locking.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("storage: opening %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db, aead: aead}
	if err := s.prepare(path); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// prepare creates or checks the layout, restricts the file, and proves the
// key by reading every Conversation row back.
func (s *Store) prepare(path string) error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("storage: reading the schema version of %s: %w", path, err)
	}
	if version > schemaVersion {
		return fmt.Errorf("storage: %s is schema version %d, and this build reads version %d", path, version, schemaVersion)
	}
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("storage: creating the schema in %s: %w", path, err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("storage: stamping the schema version of %s: %w", path, err)
	}
	if err := os.Chmod(path, fileMode); err != nil {
		return fmt.Errorf("storage: securing %s: %w", path, err)
	}
	if _, err := s.conversationRows(s.db); err != nil {
		return fmt.Errorf("%w (%s)", err, path)
	}
	return nil
}

// Close releases the database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("storage: closing the database: %w", err)
	}
	return nil
}

// row is one conversations row, decrypted.
type row struct {
	id   int64
	peer identity.PublicKey
	name string
}

// querier is the slice of *sql.DB and *sql.Tx that reading needs.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// conversationRows reads and decrypts every conversations row. Linear, but
// a device holds one row per person its owner talks to; an index would be a
// deterministic fingerprint of a key or a name on disk, which is exactly
// what the encryption is there to avoid.
func (s *Store) conversationRows(q querier) ([]row, error) {
	res, err := q.Query("SELECT id, peer_key, name FROM conversations ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("storage: reading conversations: %w", err)
	}
	defer res.Close()

	var out []row
	for res.Next() {
		var (
			r         row
			key, name []byte
		)
		if err := res.Scan(&r.id, &key, &name); err != nil {
			return nil, fmt.Errorf("storage: reading conversations: %w", err)
		}
		rawKey, err := unseal(s.aead, "conversations", "peer_key", r.id, key)
		if err != nil {
			return nil, err
		}
		r.peer, err = identity.ParsePublicKey(string(rawKey))
		if err != nil {
			return nil, fmt.Errorf("storage: conversation %d: %w", r.id, err)
		}
		rawName, err := unseal(s.aead, "conversations", "name", r.id, name)
		if err != nil {
			return nil, err
		}
		r.name = string(rawName)
		out = append(out, r)
	}
	if err := res.Err(); err != nil {
		return nil, fmt.Errorf("storage: reading conversations: %w", err)
	}
	return out, nil
}

// findPeer returns the Conversation pinned to peer, if one is.
func findPeer(rows []row, peer identity.PublicKey) (row, bool) {
	for _, r := range rows {
		if r.peer == peer {
			return r, true
		}
	}
	return row{}, false
}

// findName returns the Conversation named name, if one is.
func findName(rows []row, name string) (row, bool) {
	for _, r := range rows {
		if r.name == name {
			return r, true
		}
	}
	return row{}, false
}

// sealInto encrypts value for one cell and writes it there. Encrypting needs
// the rowid the additional data binds, so inserts go placeholder-first and
// the real values land through here.
func sealInto(tx *sql.Tx, aead cipher.AEAD, table, column string, rowid int64, value []byte) error {
	box, err := seal(aead, table, column, rowid, value)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", table, column), box, rowid); err != nil {
		return fmt.Errorf("storage: writing %s.%s: %w", table, column, err)
	}
	return nil
}

// Pinned implements session.Pins: the Identity name was last accepted with.
func (s *Store) Pinned(name string) (identity.PublicKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.conversationRows(s.db)
	if err != nil {
		// Open proved the key against this database, so this is the
		// database going bad underneath a running Session. Claiming "never
		// met" here would downgrade a returning Peer's "Security Code
		// changed" warning to a fresh first meeting; claiming a pin that
		// cannot be read is impossible. The least dishonest answer left is
		// unknown.
		return identity.PublicKey{}, false
	}
	if r, ok := findName(rows, name); ok {
		return r.peer, true
	}
	return identity.PublicKey{}, false
}

// Pin implements session.Pins: name was accepted with key. A known name
// under a new key is the accepted Identity change, and the Conversation
// follows it — the row is repinned, the history stays. A known key under a
// new name is a rename. Anyone else is a first meeting and a fresh row.
func (s *Store) Pin(name string, key identity.PublicKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transact(func(tx *sql.Tx) error {
		rows, err := s.conversationRows(tx)
		if err != nil {
			return err
		}
		if r, ok := findName(rows, name); ok {
			if r.peer == key {
				return nil
			}
			return sealInto(tx, s.aead, "conversations", "peer_key", r.id, []byte(key.String()))
		}
		if r, ok := findPeer(rows, key); ok {
			return sealInto(tx, s.aead, "conversations", "name", r.id, []byte(name))
		}
		_, err = insertConversation(tx, s.aead, name, key)
		return err
	})
}

// insertConversation creates the row a first meeting needs: placeholders
// first, then the encrypted values once the rowid they bind to exists.
func insertConversation(tx *sql.Tx, aead cipher.AEAD, name string, key identity.PublicKey) (int64, error) {
	res, err := tx.Exec("INSERT INTO conversations (peer_key, name) VALUES (x'', x'')")
	if err != nil {
		return 0, fmt.Errorf("storage: creating a conversation: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: creating a conversation: %w", err)
	}
	if err := sealInto(tx, aead, "conversations", "peer_key", id, []byte(key.String())); err != nil {
		return 0, err
	}
	if err := sealInto(tx, aead, "conversations", "name", id, []byte(name)); err != nil {
		return 0, err
	}
	return id, nil
}

// Outgoing implements session.History: keep a message this side is sending.
func (s *Store) Outgoing(peer identity.PublicKey, name, id, body string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transact(func(tx *sql.Tx) error {
		_, err := s.insertMessage(tx, peer, name, id, body, at, true, session.TextPending)
		return err
	})
}

// Incoming implements session.History: keep a message the Peer sent, before
// it is acknowledged. fresh is false for a wire id already kept.
func (s *Store) Incoming(peer identity.PublicKey, name, id, body string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh := true
	err := s.transact(func(tx *sql.Tx) error {
		var err error
		fresh, err = s.insertMessage(tx, peer, name, id, body, at, false, session.TextDelivered)
		return err
	})
	return fresh, err
}

// insertMessage keeps one message under peer's Conversation, creating the
// Conversation — under the Display Name the Session knows peer by — if the
// pin is not there, as after a mid-Session clearhistory. A message must
// never be dropped for want of a row to hang it on.
func (s *Store) insertMessage(tx *sql.Tx, peer identity.PublicKey, name, id, body string, at time.Time, mine bool, status session.DeliveryStatus) (fresh bool, err error) {
	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM messages WHERE msg_id = ?", id).Scan(&exists); err != nil {
		return false, fmt.Errorf("storage: checking message %s: %w", id, err)
	}
	if exists > 0 {
		return false, nil
	}

	conversation, err := s.conversationFor(tx, peer, name)
	if err != nil {
		return false, err
	}
	res, err := tx.Exec(
		"INSERT INTO messages (conversation_id, msg_id, mine, body, status, at) VALUES (?, ?, ?, x'', ?, ?)",
		conversation, id, mine, int(status), at.UnixMilli(),
	)
	if err != nil {
		return false, fmt.Errorf("storage: keeping message %s: %w", id, err)
	}
	rowid, err := res.LastInsertId()
	if err != nil {
		return false, fmt.Errorf("storage: keeping message %s: %w", id, err)
	}
	return true, sealInto(tx, s.aead, "messages", "body", rowid, []byte(body))
}

// conversationFor finds the Conversation pinned to peer, creating one under
// name if none is.
func (s *Store) conversationFor(tx *sql.Tx, peer identity.PublicKey, name string) (int64, error) {
	rows, err := s.conversationRows(tx)
	if err != nil {
		return 0, err
	}
	if r, ok := findPeer(rows, peer); ok {
		return r.id, nil
	}
	return insertConversation(tx, s.aead, name, peer)
}

// Status implements session.History: move a kept message's delivery along. A
// wire id never kept is quietly nothing — an ack for a message from a
// Session that kept no Log, say.
func (s *Store) Status(id string, status session.DeliveryStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec("UPDATE messages SET status = ? WHERE msg_id = ?", int(status), id); err != nil {
		return fmt.Errorf("storage: recording the status of %s: %w", id, err)
	}
	return nil
}

// Conversations lists every kept Conversation, oldest pin first.
func (s *Store) Conversations() ([]Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.conversationRows(s.db)
	if err != nil {
		return nil, err
	}
	out := make([]Conversation, 0, len(rows))
	for _, r := range rows {
		c := Conversation{Peer: r.peer, Name: r.name}
		var last sql.NullInt64
		err := s.db.QueryRow(
			"SELECT COUNT(*), MAX(at) FROM messages WHERE conversation_id = ?", r.id,
		).Scan(&c.Messages, &last)
		if err != nil {
			return nil, fmt.Errorf("storage: counting messages: %w", err)
		}
		if last.Valid {
			c.LastAt = time.UnixMilli(last.Int64)
		}
		out = append(out, c)
	}
	return out, nil
}

// Messages reads peer's Conversation back, oldest first, decrypted.
func (s *Store) Messages(peer identity.PublicKey) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.conversationRows(s.db)
	if err != nil {
		return nil, err
	}
	if r, ok := findPeer(rows, peer); ok {
		return s.messagesOf(r.id)
	}
	return nil, nil
}

// messagesOf reads one Conversation's messages. Callers hold s.mu.
func (s *Store) messagesOf(conversation int64) ([]Message, error) {
	res, err := s.db.Query(
		"SELECT id, msg_id, mine, body, status, at FROM messages WHERE conversation_id = ? ORDER BY id", conversation,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: reading messages: %w", err)
	}
	defer res.Close()

	var out []Message
	for res.Next() {
		var (
			m      Message
			rowid  int64
			body   []byte
			status int
			at     int64
		)
		if err := res.Scan(&rowid, &m.ID, &m.Mine, &body, &status, &at); err != nil {
			return nil, fmt.Errorf("storage: reading messages: %w", err)
		}
		raw, err := unseal(s.aead, "messages", "body", rowid, body)
		if err != nil {
			return nil, err
		}
		m.Body = string(raw)
		m.Status = session.DeliveryStatus(status)
		m.At = time.UnixMilli(at)
		out = append(out, m)
	}
	if err := res.Err(); err != nil {
		return nil, fmt.Errorf("storage: reading messages: %w", err)
	}
	return out, nil
}

// Clear deletes peer's Conversation — the messages, the pin and the name —
// and vacuums, so the freed pages leave the file rather than lingering as
// recoverable ciphertext. It removes only this device's copy; the Peer
// still has theirs. A peer with nothing kept is quietly nothing.
func (s *Store) Clear(peer identity.PublicKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.transact(func(tx *sql.Tx) error {
		rows, err := s.conversationRows(tx)
		if err != nil {
			return err
		}
		r, ok := findPeer(rows, peer)
		if !ok {
			return nil
		}
		if _, err := tx.Exec("DELETE FROM messages WHERE conversation_id = ?", r.id); err != nil {
			return fmt.Errorf("storage: clearing messages: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM conversations WHERE id = ?", r.id); err != nil {
			return fmt.Errorf("storage: clearing the conversation: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, err := s.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("storage: vacuuming: %w", err)
	}
	return nil
}

// transact runs fn in one transaction, so a Pin or a kept message is on
// disk whole or not at all.
func (s *Store) transact(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("storage: starting a transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w (and rolling back: %v)", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: committing: %w", err)
	}
	return nil
}
