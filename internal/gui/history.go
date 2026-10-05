package gui

import (
	"fmt"
	"time"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/storage"
	"github.com/DSNR/dcc/internal/words"
)

// The stored Conversations, as the window offers them: a panel listing who
// there is to read back, with each one openable into the conversation area
// and deletable from this device. Every Store read happens on a goroutine of
// its own — SQLite on a slow disk must not stop the window painting.

// RefreshHistory re-reads the list of stored Conversations.
func (m *Model) RefreshHistory() {
	if m.store == nil {
		return
	}
	go func() {
		conversations, err := m.store.Conversations()
		if err != nil {
			m.say("Could not read the history: " + err.Error())
			return
		}
		listed := make([]Conversation, 0, len(conversations))
		for _, c := range conversations {
			listed = append(listed, conversation(c))
		}
		m.mu.Lock()
		m.conversations = listed
		m.mu.Unlock()
		m.changed()
	}()
}

// Open reads one stored Conversation into the conversation area, connected or
// not — history is this device's own, and needs nobody on the other end. It
// reads each Conversation in once: a second Read would print the same
// messages under the first copy, which reads as them having been said twice.
func (m *Model) Open(c Conversation) {
	if m.store == nil {
		m.say(noStore)
		return
	}
	m.mu.Lock()
	shown := m.shown[c.Peer]
	m.shown[c.Peer] = true
	m.mu.Unlock()
	if shown {
		m.say(fmt.Sprintf("The Conversation with %s is already above — scroll back for it.", words.Quoted(c.Name)))
		return
	}
	go func() {
		messages, err := m.store.Messages(c.Peer)
		if err != nil {
			m.say("Could not read the history: " + err.Error())
			return
		}
		m.mu.Lock()
		if len(messages) == 0 {
			m.add(notice(fmt.Sprintf("Nothing has been said with %s yet.", words.Quoted(c.Name))))
		} else {
			m.add(notice(fmt.Sprintf("The Conversation with %s:", words.Quoted(c.Name))))
			m.showMessages(messages, c.Name)
		}
		m.mu.Unlock()
		m.changed()
	}()
}

// Clear deletes one Conversation from this device. The window asks first —
// this is the one button in dcc that destroys something.
func (m *Model) Clear(c Conversation) {
	if m.store == nil {
		m.say(noStore)
		return
	}
	go func() {
		if err := m.store.Clear(c.Peer); err != nil {
			m.say("Could not clear the history: " + err.Error())
			return
		}
		// What is on screen stays there — it is a record of what happened in
		// front of the participant — but the deleted Conversation can be read
		// in again if it is ever stored afresh.
		m.mu.Lock()
		delete(m.shown, c.Peer)
		m.mu.Unlock()
		m.say(fmt.Sprintf(
			"Deleted the Conversation with %s from this device — their copy, and their next Session here, start fresh.",
			words.Quoted(c.Name)))
		m.RefreshHistory()
	}()
}

// noStore is what every history control says when dcc is running without
// storage — which the real binary never is.
const noStore = "No history is kept in this run."

// restoreHistory puts what has been said with an accepted Peer back on
// screen, so a restart resumes the Conversation instead of losing it.
func (m *Model) restoreHistory(peer identity.PublicKey, name string) {
	if m.store == nil {
		return
	}
	m.mu.Lock()
	shown := m.shown[peer]
	m.shown[peer] = true
	m.mu.Unlock()
	if shown {
		// Reading it in with the Session it belongs to is enough; the panel's
		// Read button would only repeat it.
		return
	}
	go func() {
		messages, err := m.store.Messages(peer)
		if err != nil {
			m.say("Could not read the history: " + err.Error())
			return
		}
		if len(messages) == 0 {
			return
		}
		m.mu.Lock()
		m.add(notice(fmt.Sprintf("The Conversation with %s so far:", words.Quoted(name))))
		m.showMessages(messages, name)
		m.mu.Unlock()
		m.changed()
	}()
}

// showMessages appends stored messages to the conversation, with a dated line
// wherever the calendar day changes — the timestamps alone carry only the
// time of day. It is called with the lock held.
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
		m.add(Entry{At: msg.At, Who: who, Mine: msg.Mine, Body: msg.Body, Status: msg.Status})
	}
}
