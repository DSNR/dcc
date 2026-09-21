# dcc

End-to-end encrypted, serverless, peer-to-peer chat and calling between two people. Cloudflare is used only to find each other, never to hold content.

## Language

**Session**:
One live encrypted connection between a Host and a Peer, from invite to disconnect.
_Avoid_: Room, call (a call happens within a session)

**Host**:
The participant who opens the Rendezvous and produces the Invite.
_Avoid_: Server, owner

**Peer**:
The participant who joins a Session using an Invite. Also the generic term for the other side once connected.
_Avoid_: Client, guest

**Participant**:
Any member of a Session, identified by a participant id. Two for now, more later.
_Avoid_: User, member

**Rendezvous**:
The temporary Cloudflare Quick Tunnel endpoint the Host exposes so the Peer can perform Signaling and, as fallback, relay data.
_Avoid_: Tunnel (implementation), server

**Invite**:
The string the Host hands to the Peer out-of-band: the Rendezvous URL plus a Password.
_Avoid_: Link, code, token

**Password**:
A shared secret carried in the Invite that gates joining the Rendezvous.
_Avoid_: PSK, key, secret

**Signaling**:
The exchange over the Rendezvous that lets peers set up a direct WebRTC connection.
_Avoid_: Handshake (that's the Noise step), negotiation

**Identity**:
A participant's persistent local keypair, used to authenticate sessions and stabilise the Security Code across sessions.
_Avoid_: Account, profile

**Security Code**:
The short human-readable string both participants can compare to verify they're talking to each other with no one in between.
_Avoid_: Fingerprint, safety number

**Conversation**:
The locally stored history of messages with a given Peer across sessions.
_Avoid_: Chat, thread, log

**Call**:
An audio/video media exchange inside a Session. Screen sharing is a stream within a Call.
_Avoid_: Meeting, video chat
