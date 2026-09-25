package session

import (
	"context"
	"errors"
	"time"

	"github.com/DSNR/dcc/internal/signaling"
	"github.com/DSNR/dcc/internal/wire"
)

// ReconnectBudget is how long a dropped connection gets to come back before
// the Session is declared over. Both sides count from the moment they notice
// the drop; a blip that outlives the budget was not a blip.
const ReconnectBudget = 60 * time.Second

// redialBase and redialCap shape the Peer's backoff between redial attempts:
// the first retry waits redialBase, and each one after doubles it up to
// redialCap.
const (
	redialBase = time.Second
	redialCap  = 8 * time.Second
)

// connectionLostLocked answers the signaling connection dying: under an
// established Session the reconnect machinery takes over; anywhere else it
// is the end it always was.
func (s *Session) connectionLostLocked() {
	if s.state == Connected || s.state == Reconnecting {
		s.enterReconnectLocked()
		return
	}
	s.endLocked(Disconnected, ReasonConnectionLost)
}

// enterReconnectLocked drops whatever connection and transport the Session
// holds and starts working on their replacements: the Host arms the budget
// and waits at its Rendezvous for the Peer's re-handshake; the Peer redials.
// Entered again while already Reconnecting — a replacement that died too —
// it keeps the original deadline: the budget is per outage, not per attempt.
func (s *Session) enterReconnectLocked() {
	s.gen++
	conn, trans := s.conn, s.trans
	s.conn, s.trans = nil, nil
	s.up = false
	go func() {
		if trans != nil {
			_ = trans.Close()
		}
		if conn != nil {
			conn.Close()
		}
	}()

	if s.state != Reconnecting {
		s.reconnectUntil = time.Now().Add(s.budget)
		s.setStateLocked(Reconnecting, ReasonNone)
	}
	if s.isHost {
		if s.reconnectTimer == nil {
			s.reconnectTimer = time.AfterFunc(time.Until(s.reconnectUntil), s.reconnectExpired)
		}
	} else if !s.redialing {
		s.redialing = true
		go s.redial()
	}
}

// reconnectExpired is the Host's budget running out with no Peer back.
func (s *Session) reconnectExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state != Reconnecting {
		return
	}
	s.endLocked(Disconnected, ReasonConnectionLost)
}

// redial is the Peer's reconnect loop: a fresh Noise handshake at the same
// Rendezvous, echoing the session_id, backing off between attempts until the
// budget runs out — or until a dial proves the Rendezvous is gone, which no
// amount of patience fixes.
func (s *Session) redial() {
	wait := redialBase
	for {
		s.mu.Lock()
		if s.closed || s.state != Reconnecting {
			s.redialing = false
			s.mu.Unlock()
			return
		}
		deadline, invite := s.reconnectUntil, s.invite
		hello := wire.PeerHello{Name: s.name, DTLS: s.cert.Fingerprint(), SessionID: s.sessionID}
		s.mu.Unlock()

		if !time.Now().Before(deadline) {
			s.giveUp(Disconnected, ReasonConnectionLost)
			return
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		conn, hostHello, err := signaling.Dial(ctx, invite.SignalURL(), signaling.DialOptions{
			Identity: s.identity,
			Password: invite.Password,
			Hello:    hello,
		})
		cancel()
		if err != nil {
			if errors.Is(err, signaling.ErrRendezvousGone) {
				s.giveUp(Failed, ReasonRendezvousGone)
				return
			}
			sleep := min(wait, time.Until(deadline))
			if sleep > 0 {
				time.Sleep(sleep)
			}
			wait = min(wait*2, redialCap)
			continue
		}

		s.mu.Lock()
		if s.closed || s.state != Reconnecting {
			s.redialing = false
			s.mu.Unlock()
			conn.Close()
			return
		}
		if conn.Peer() != s.peer || hostHello.SessionID != hello.SessionID {
			// Whoever completed that handshake, it is not the Session being
			// resumed — and the only party who could have is the Host.
			s.redialing = false
			s.endLocked(Failed, ReasonHandshakeFailed)
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.redialing = false
		s.attachLocked(conn, hostHello.Name, hostHello.DTLS, true, true)
		s.mu.Unlock()
		return
	}
}

// giveUp ends a reconnect that isn't going to happen, unless the Session
// already moved on without it.
func (s *Session) giveUp(state State, reason Reason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.redialing = false
	if s.closed || s.state != Reconnecting {
		return
	}
	s.endLocked(state, reason)
}

// resendLocked puts everything still unacknowledged back on the wire, in
// send order. The other side dedups by id, so a message whose ack was what
// the blip ate arrives twice and shows once.
func (s *Session) resendLocked() {
	for i := range s.queue {
		m := &s.queue[i]
		if err := s.trans.Send(wire.Text{ID: m.id, Body: m.body}); err != nil {
			// The transport is dying already; the next reconnect retries.
			return
		}
		if !m.sent {
			m.sent = true
			s.status(m.id, TextSent)
		}
	}
}
