package transport

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"

	"github.com/DSNR/dcc/internal/wire"
)

// The protocol's Signaling deadlines.
const (
	// OfferTimeout is how long the Host waits for the Peer's offer after the
	// handshake. The Peer sends it immediately, so a missing offer means the
	// other side is not doing WebRTC at all.
	OfferTimeout = 10 * time.Second
	// ConnectTimeout is how long ICE gets to produce a working connection
	// before the attempt is declared dead.
	ConnectTimeout = 30 * time.Second
)

// channelLabel names the DataChannel both sides pre-agree on — ordered,
// reliable, id 0 — so it is never announced in-band.
const channelLabel = "dcc"

// stunServers are the public STUN servers used to discover this side's
// reflexive address. STUN only — no TURN, per the spec; relaying is the
// Rendezvous's job.
var stunServers = []string{
	"stun:stun.l.google.com:19302",
	"stun:stun.cloudflare.com:3478",
}

// Link is Connected's sub-status: how content is actually flowing.
type Link int

const (
	// LinkDirect: the selected candidate pair goes peer to peer.
	LinkDirect Link = iota + 1
	// LinkRelayed: content is flowing through the Rendezvous's fallback
	// relay. In dcc that is any TCP candidate pair — ICE-TCP exists here
	// only as the relay path; there is no TURN.
	LinkRelayed
)

// String implements fmt.Stringer.
func (l Link) String() string {
	switch l {
	case LinkDirect:
		return "direct"
	case LinkRelayed:
		return "relayed"
	}
	return fmt.Sprintf("Link(%d)", int(l))
}

// Options configures a Transport. The callbacks arrive on the Transport's own
// goroutines; none of them is ever called again after Down or Close.
type Options struct {
	// Certificate is this side's DTLS identity, whose fingerprint the local
	// hello carried.
	Certificate Certificate
	// Remote is the fingerprint the other side's hello bound. A remote
	// certificate hashing to anything else kills the Transport: whatever
	// terminated DTLS, it was not the Identity the handshake authenticated.
	Remote string
	// Initiator marks the Peer's side, which sends the offer immediately.
	Initiator bool
	// Signal sends one Signaling frame — offer, answer or ice — to the other
	// side over the Noise transport.
	Signal func(wire.Frame)
	// Up fires once, when the DataChannel is open and the remote certificate
	// matches Remote.
	Up func(Link)
	// Frame delivers each decoded DataChannel frame.
	Frame func(wire.Frame)
	// Down fires at most once, when the Transport dies — before or after Up,
	// but never after Close.
	Down func(error)
	// OfferWait and ConnectWait override OfferTimeout and ConnectTimeout;
	// zero means the protocol value. Tests shorten them.
	OfferWait, ConnectWait time.Duration
	// RelayListener is the Host's side of the fallback relay: a listener
	// whose connections arrive through the Rendezvous, fed to pion's TCPMux
	// so ICE-TCP runs over them. Non-nil hands its ownership to the
	// Transport, which closes it when it ends.
	RelayListener net.Listener
	// RelayAddr is the Peer's side of the fallback relay: the local bridge
	// that reaches the Rendezvous. Non-empty, every passive TCP candidate
	// the other side advertises is rewritten to it before pion dials.
	RelayAddr string
	// RelayOnly disables every direct path — UDP and STUN — leaving the
	// fallback relay as the only route. Tests use it to prove the relay
	// carries a Session alone.
	RelayOnly bool
}

// Transport is one WebRTC PeerConnection's worth of connectivity: the
// DataChannel now, media later. It lives inside a Session, from the Noise
// handshake completing to the Session ending.
type Transport struct {
	opts Options
	pc   *webrtc.PeerConnection
	dc   *webrtc.DataChannel
	// mux is the Host's ICE-TCP mux over the relay listener, nil on the
	// Peer's side and when there is no relay at all.
	mux ice.TCPMux

	mu sync.Mutex
	// descSent gates outgoing candidates: trickled ICE must never overtake
	// the offer or answer it belongs to on the ordered Noise transport.
	descSent bool
	heldOut  []wire.Frame
	// remoteSet gates incoming candidates the same way pion requires: a
	// candidate cannot be added before the remote description exists.
	remoteSet bool
	heldIn    []webrtc.ICECandidateInit
	ended     bool

	offerTimer, connectTimer *time.Timer
}

// Start builds the PeerConnection and the DataChannel and, on the initiator's
// side, sends the offer before returning. From here the Transport drives
// itself: feed it the other side's frames with HandleSignal and it reports
// through the Options callbacks.
func Start(opts Options) (*Transport, error) {
	if opts.OfferWait == 0 {
		opts.OfferWait = OfferTimeout
	}
	if opts.ConnectWait == 0 {
		opts.ConnectWait = ConnectTimeout
	}

	// mDNS candidate obfuscation exists for browsers that must hide LAN
	// addresses from web pages; between two dcc installs it only adds a
	// resolution step that can fail.
	var se webrtc.SettingEngine
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)

	servers := []webrtc.ICEServer{{URLs: stunServers}}
	var mux ice.TCPMux
	if opts.RelayListener != nil || opts.RelayAddr != "" {
		// The relay lives on loopback at both ends — the Host's mux address
		// and the Peer's bridge — so loopback gathering is what makes pion
		// advertise the one and dial the other.
		se.SetIncludeLoopbackCandidate(true)
		networks := []webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6, webrtc.NetworkTypeTCP4}
		if opts.RelayOnly {
			networks = []webrtc.NetworkType{webrtc.NetworkTypeTCP4}
			servers = nil
		}
		se.SetNetworkTypes(networks)
		if opts.RelayListener != nil {
			mux = ice.NewTCPMuxDefault(ice.TCPMuxParams{Listener: opts.RelayListener, ReadBufferSize: 8})
			se.SetICETCPMux(mux)
		}
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))

	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers:   servers,
		Certificates: []webrtc.Certificate{opts.Certificate.cert},
	})
	if err != nil {
		if mux != nil {
			_ = mux.Close()
		}
		return nil, fmt.Errorf("transport: building the PeerConnection: %w", err)
	}

	t := &Transport{opts: opts, pc: pc, mux: mux}

	negotiated, id := true, uint16(0)
	dc, err := pc.CreateDataChannel(channelLabel, &webrtc.DataChannelInit{Negotiated: &negotiated, ID: &id})
	if err != nil {
		_ = pc.Close()
		if mux != nil {
			_ = mux.Close()
		}
		return nil, fmt.Errorf("transport: creating the %s DataChannel: %w", channelLabel, err)
	}
	t.dc = dc

	dc.OnOpen(t.onOpen)
	dc.OnMessage(t.onMessage)
	dc.OnClose(func() { t.fail(errors.New("transport: the DataChannel closed")) })
	pc.OnICECandidate(t.onCandidate)
	pc.OnConnectionStateChange(t.onConnectionState)

	// The timers are armed under the lock because their callbacks read the
	// fields they are being assigned to.
	t.mu.Lock()
	t.connectTimer = time.AfterFunc(opts.ConnectWait, func() {
		t.fail(fmt.Errorf("transport: no connection within %v", opts.ConnectWait))
	})
	if !opts.Initiator {
		t.offerTimer = time.AfterFunc(opts.OfferWait, func() {
			t.fail(fmt.Errorf("transport: no offer within %v", opts.OfferWait))
		})
	}
	t.mu.Unlock()

	if opts.Initiator {
		if err := t.offer(); err != nil {
			t.Close()
			return nil, err
		}
	}
	return t, nil
}

// HandleSignal feeds the Transport one Signaling frame from the other side.
// Frames it has no use for are ignored; a frame that breaks the exchange —
// an SDP pion cannot apply — takes the Transport down.
func (t *Transport) HandleSignal(f wire.Frame) {
	switch f := f.(type) {
	case wire.Offer:
		t.onOffer(f)
	case wire.Answer:
		t.onAnswer(f)
	case wire.ICE:
		t.onRemoteCandidate(f)
	}
}

// Send encodes one frame and hands it to the DataChannel, which delivers it
// ordered and reliably — or errors here if the channel is gone.
func (t *Transport) Send(f wire.Frame) error {
	b, err := wire.Encode(f)
	if err != nil {
		return err
	}
	if err := t.dc.Send(b); err != nil {
		return fmt.Errorf("transport: sending a %s frame: %w", f.Type(), err)
	}
	return nil
}

// Close tears the Transport down without a Down callback: the owner asked, so
// the owner knows. Closing twice is fine.
func (t *Transport) Close() error {
	if !t.end() {
		return nil
	}
	return t.pc.Close()
}

// offer is the initiator's opening move: local description first, then the
// offer frame, and candidates trickle behind it.
func (t *Transport) offer() error {
	offer, err := t.pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("transport: creating the offer: %w", err)
	}
	if err := t.pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("transport: applying the local offer: %w", err)
	}
	t.sendDescription(wire.Offer{SDP: offer.SDP})
	return nil
}

// onOffer is the responder receiving the initiator's offer: apply it, answer
// it, release the candidates held on both gates.
func (t *Transport) onOffer(f wire.Offer) {
	t.mu.Lock()
	if t.offerTimer != nil {
		t.offerTimer.Stop()
	}
	initiator := t.opts.Initiator
	t.mu.Unlock()
	if initiator {
		// Renegotiation arrives with the Call work; until then an offer at
		// the initiator is noise.
		return
	}
	if err := t.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: f.SDP}); err != nil {
		t.fail(fmt.Errorf("transport: applying the remote offer: %w", err))
		return
	}
	answer, err := t.pc.CreateAnswer(nil)
	if err != nil {
		t.fail(fmt.Errorf("transport: creating the answer: %w", err))
		return
	}
	if err := t.pc.SetLocalDescription(answer); err != nil {
		t.fail(fmt.Errorf("transport: applying the local answer: %w", err))
		return
	}
	t.sendDescription(wire.Answer{SDP: answer.SDP})
	t.releaseRemoteCandidates()
}

// onAnswer is the initiator receiving the responder's answer.
func (t *Transport) onAnswer(f wire.Answer) {
	if !t.opts.Initiator {
		return
	}
	if err := t.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: f.SDP}); err != nil {
		t.fail(fmt.Errorf("transport: applying the remote answer: %w", err))
		return
	}
	t.releaseRemoteCandidates()
}

// onCandidate trickles one gathered candidate to the other side, or holds it
// while this side's description is still unsent. nil is pion saying gathering
// is over, which the wire spells as an empty candidate.
func (t *Transport) onCandidate(c *webrtc.ICECandidate) {
	f := wire.ICE{}
	if c != nil {
		init := c.ToJSON()
		f.Candidate = init.Candidate
		if init.SDPMid != nil {
			f.Mid = *init.SDPMid
		}
		if init.SDPMLineIndex != nil {
			f.MLine = int(*init.SDPMLineIndex)
		}
	}
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	if !t.descSent {
		t.heldOut = append(t.heldOut, f)
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	t.opts.Signal(f)
}

// sendDescription puts the offer or answer on the wire and then releases any
// candidates that gathered while it was pending, preserving their order.
func (t *Transport) sendDescription(desc wire.Frame) {
	t.opts.Signal(desc)
	t.mu.Lock()
	t.descSent = true
	held := t.heldOut
	t.heldOut = nil
	t.mu.Unlock()
	for _, f := range held {
		t.opts.Signal(f)
	}
}

// onRemoteCandidate applies one trickled candidate, holding it if the remote
// description hasn't arrived yet. The empty end-of-candidates marker needs no
// action: pion treats a quiet peer the same way.
func (t *Transport) onRemoteCandidate(f wire.ICE) {
	if f.Candidate == "" {
		return
	}
	candidate := f.Candidate
	if t.opts.RelayAddr != "" {
		candidate = rewriteRelayCandidate(candidate, t.opts.RelayAddr)
	}
	mid, mline := f.Mid, uint16(f.MLine)
	init := webrtc.ICECandidateInit{Candidate: candidate, SDPMid: &mid, SDPMLineIndex: &mline}
	t.mu.Lock()
	if !t.remoteSet {
		t.heldIn = append(t.heldIn, init)
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	// A candidate that won't apply is one route lost, not the exchange dead.
	_ = t.pc.AddICECandidate(init)
}

// rewriteRelayCandidate points a passive TCP candidate at addr. The other
// side's relay listener sits behind its Rendezvous, not at the address the
// candidate names — the local bridge at addr is what actually reaches it.
// Anything else — UDP, active TCP, a shape this doesn't recognise — passes
// through untouched.
func rewriteRelayCandidate(candidate, addr string) string {
	fields := strings.Fields(candidate)
	// candidate:<foundation> <component> <transport> <priority> <address>
	// <port> typ <type>, then extension pairs — tcptype among them.
	if len(fields) < 8 || !strings.EqualFold(fields[2], "tcp") {
		return candidate
	}
	passive := false
	for i := 8; i+1 < len(fields); i += 2 {
		if fields[i] == "tcptype" && fields[i+1] == "passive" {
			passive = true
			break
		}
	}
	if !passive {
		return candidate
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return candidate
	}
	fields[4], fields[5] = host, port
	return strings.Join(fields, " ")
}

// releaseRemoteCandidates opens the incoming gate once the remote description
// is in place.
func (t *Transport) releaseRemoteCandidates() {
	t.mu.Lock()
	t.remoteSet = true
	held := t.heldIn
	t.heldIn = nil
	t.mu.Unlock()
	for _, init := range held {
		_ = t.pc.AddICECandidate(init)
	}
}

// onOpen is the DataChannel opening: the one place the Transport comes Up,
// and the moment the remote certificate is checked against the fingerprint
// the handshake bound.
func (t *Transport) onOpen() {
	der := t.pc.SCTP().Transport().GetRemoteCertificate()
	if len(der) == 0 {
		t.fail(errors.New("transport: no remote DTLS certificate to verify"))
		return
	}
	if got := fingerprintDER(der); got != t.opts.Remote {
		t.fail(fmt.Errorf("transport: the remote DTLS certificate is %s, not the %s the handshake bound", got, t.opts.Remote))
		return
	}

	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return
	}
	t.connectTimer.Stop()
	t.mu.Unlock()
	t.opts.Up(t.link())
}

// link reads the selected candidate pair's verdict: relayed if either end is
// a relay candidate or the pair runs over TCP — dcc's only TCP path is the
// Rendezvous relay — direct otherwise.
func (t *Transport) link() Link {
	pair, err := t.pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return LinkDirect
	}
	if (pair.Local != nil && (pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Local.Protocol == webrtc.ICEProtocolTCP)) ||
		(pair.Remote != nil && (pair.Remote.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Protocol == webrtc.ICEProtocolTCP)) {
		return LinkRelayed
	}
	return LinkDirect
}

// onMessage decodes one DataChannel message. Survivable problems — unknown
// types, malformed droppable frames — are consumed here; a frame the
// connection cannot continue past takes the Transport down.
func (t *Transport) onMessage(msg webrtc.DataChannelMessage) {
	f, err := wire.Decode(wire.PathData, msg.Data)
	if err != nil {
		if wire.DispositionOf(err) == wire.CloseConnection {
			t.fail(err)
		}
		return
	}
	t.opts.Frame(f)
}

// onConnectionState watches for the states pion won't come back from.
// Disconnected is not among them — ICE may recover it, and if it doesn't,
// Failed follows.
func (t *Transport) onConnectionState(s webrtc.PeerConnectionState) {
	switch s {
	case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
		t.fail(fmt.Errorf("transport: the PeerConnection is %s", s))
	}
}

// fail ends the Transport and tells the owner why, exactly once. The
// PeerConnection is closed off this goroutine because pion invokes fail's
// callers from goroutines Close waits on.
func (t *Transport) fail(err error) {
	if !t.end() {
		return
	}
	t.opts.Down(err)
	go func() { _ = t.pc.Close() }()
}

// end claims the one transition into ended, stopping the timers on the way.
// The relay mux goes down with the Transport — off this goroutine, because
// closing it waits for connection handlers pion may be calling us from.
func (t *Transport) end() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return false
	}
	t.ended = true
	if t.offerTimer != nil {
		t.offerTimer.Stop()
	}
	t.connectTimer.Stop()
	if t.mux != nil {
		mux := t.mux
		go func() { _ = mux.Close() }()
	}
	return true
}
