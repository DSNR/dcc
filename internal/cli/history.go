package cli

import (
	"fmt"
	"time"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/storage"
	"github.com/DSNR/dcc/internal/words"
)

// Store is the slice of *storage.Store the terminal interface reads history
// through. Like Session, it is an interface so that the tests can put a fake
// underneath what is shown.
type Store interface {
	// Conversations lists every stored Conversation.
	Conversations() ([]storage.Conversation, error)
	// Messages reads one Conversation back, oldest first.
	Messages(peer identity.PublicKey) ([]storage.Message, error)
	// Clear deletes one Conversation — this device's copy only.
	Clear(peer identity.PublicKey) error
}

// noStore is what every history command says when dcc is running without
// storage — which the real binary never is.
const noStore = "No history is kept in this run."

// couldNotRead reports a Store read failing, wherever one is read.
func (m *Model) couldNotRead(err error) {
	m.add(notice("Could not read the history: " + err.Error()))
}

// nothingStored is the answer to a Display Name no Conversation carries.
func (m *Model) nothingStored(name string) {
	m.add(notice(fmt.Sprintf("Nothing is stored with %s. /history alone lists what there is.", words.Quoted(name))))
}

// named filters the stored Conversations to those a Peer named name — more
// than one only when different Peers claimed the same Display Name.
func named(conversations []storage.Conversation, name string) []storage.Conversation {
	var out []storage.Conversation
	for _, c := range conversations {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// showHistory prints the stored Conversations, or one of them by Display
// Name.
func (m *Model) showHistory(name string) {
	if m.store == nil {
		m.add(notice(noStore))
		return
	}
	conversations, err := m.store.Conversations()
	if err != nil {
		m.couldNotRead(err)
		return
	}
	if name == "" {
		m.listConversations(conversations)
		return
	}
	matches := named(conversations, name)
	if len(matches) == 0 {
		m.nothingStored(name)
		return
	}
	for _, c := range matches {
		m.showConversation(c)
	}
}

// listConversations is /history with no name: who there is to read back.
func (m *Model) listConversations(conversations []storage.Conversation) {
	if len(conversations) == 0 {
		m.add(notice("Nothing is stored yet — history appears once a Conversation has happened."))
		return
	}
	m.add(notice("Stored Conversations — /history <name> reads one back:"))
	for _, c := range conversations {
		m.add(notice(fmt.Sprintf("  %s — %s", words.Quoted(c.Name), words.Stored(c.Messages, c.LastAt))))
	}
}

// showConversation reads one Conversation into the conversation view.
func (m *Model) showConversation(c storage.Conversation) {
	messages, err := m.store.Messages(c.Peer)
	if err != nil {
		m.couldNotRead(err)
		return
	}
	if len(messages) == 0 {
		m.add(notice(fmt.Sprintf("Nothing has been said with %s yet.", words.Quoted(c.Name))))
		return
	}
	m.add(notice(fmt.Sprintf("The Conversation with %s:", words.Quoted(c.Name))))
	m.showMessages(messages, c.Name)
}

// showMessages appends stored messages to the conversation, with a dated
// line wherever the calendar day changes — the timestamps alone carry only
// the time of day.
func (m *Model) showMessages(messages []storage.Message, name string) {
	var day time.Time
	for _, msg := range messages {
		if day.IsZero() || !words.SameDay(day, msg.At) {
			day = msg.At
			m.add(notice(words.Day(day)))
		}
		who := name
		if msg.Mine {
			who = words.Me
		}
		m.add(entry{at: msg.At, who: who, mine: msg.Mine, body: msg.Body, status: msg.Status})
	}
}

// clearHistory deletes one Conversation from this device.
func (m *Model) clearHistory(name string) {
	if m.store == nil {
		m.add(notice(noStore))
		return
	}
	if name == "" {
		m.add(notice("/clearhistory <name> — say whose Conversation to delete. /history lists them."))
		return
	}
	conversations, err := m.store.Conversations()
	if err != nil {
		m.couldNotRead(err)
		return
	}
	matches := named(conversations, name)
	if len(matches) == 0 {
		m.nothingStored(name)
		return
	}
	for _, c := range matches {
		if err := m.store.Clear(c.Peer); err != nil {
			m.add(notice("Could not clear the history: " + err.Error()))
			return
		}
	}
	m.add(notice(fmt.Sprintf(
		"Deleted the Conversation with %s from this device — their copy, and their next Session here, start fresh.",
		words.Quoted(name))))
}

// restoreHistory puts what has been said with an accepted Peer back on
// screen, so a restart resumes the Conversation instead of losing it.
func (m *Model) restoreHistory(peer identity.PublicKey, name string) {
	if m.store == nil {
		return
	}
	messages, err := m.store.Messages(peer)
	if err != nil {
		m.couldNotRead(err)
		return
	}
	if len(messages) == 0 {
		return
	}
	m.add(notice(fmt.Sprintf("The Conversation with %s so far:", words.Quoted(name))))
	m.showMessages(messages, name)
}
