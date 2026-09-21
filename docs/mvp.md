# MVP Specification — E2EE P2P Serverless Chat

## language

This will be built using Go and binaries compiled for windows and linux

## Goal

Build a lightweight, cross-platform, end-to-end encrypted peer-to-peer chat application for:

* Windows
* Linux
* Terminal CLI
* Windowed desktop GUI

The MVP supports direct **1-to-1 communication only**, while the architecture should avoid blocking future group/multi-user support.

There should be **no central application server storing messages, media, accounts, or conversation data**.

Cloudflare Tunnel / `cloudflared` should be used to assist users in establishing network connectivity.

---

## MVP Features

### 1. User Connection

Users must be able to establish a direct session with another user.

Basic flow:

1. User A starts a session.
2. Application starts/configures `cloudflared`.
3. User A receives a temporary connection/session identifier or invite string.
4. User A sends this to User B through another channel.
5. User B enters/opens the invite.
6. Peers authenticate each other and establish an encrypted session.

No permanent account system is required for MVP.

The application should clearly display:

* Connecting
* Connected
* Disconnected
* Connection failed

Reconnect after temporary network interruption where practical.

---

## 2. End-to-End Encryption

All user content must be encrypted before leaving the sender's application.

This includes:

* Text
* Emojis
* Video
* Audio
* Screen sharing
* Session/control messages where practical

Cloudflare or any intermediary must not be able to read message or media content.

Do not invent custom cryptography.

Use established cryptographic libraries/protocols.

Each session should use ephemeral/session-specific encryption keys.

Provide users with a simple way to verify the remote user's session identity, for example:

`Security code: 4821 7294 1163`

---

## 3. Text Chat

Required:

* Send/receive text messages
* Unicode support
* Emoji support
* Multiline messages
* Timestamps
* Basic delivery/error status
* Scrollable conversation history
* Persistent local chat history

CLI example:

`Alice: Hello 👋`

GUI should provide a conventional chat window with:

* Conversation area
* Text input
* Send button
* Emoji input/picker or native emoji support

### Local Chat History

Chat history may be persisted **locally on each user's device**.

Requirements:

* No remote/server-side history storage
* Each user stores their own copy of the conversation
* History survives application restarts
* Store conversations in a local application data directory
* Use a simple structured format such as SQLite or a local structured data file
* Messages should include at minimum:

  * Conversation/session ID
  * Participant ID
  * Direction: sent/received
  * Timestamp
  * Message content
  * Delivery status where available

Prefer SQLite if it does not materially increase complexity.

Local history should ideally be encrypted at rest using a locally held key, but this may be treated as a secondary MVP security feature if it significantly complicates implementation.

Provide a simple way to:

* View previous conversations
* Clear/delete local conversation history

Deleting history should only delete that user's local copy.

---

## 4. Video/Audio Calling

One-to-one real-time video calling.

Required:

* Microphone audio
* Webcam video
* Mute/unmute
* Camera on/off
* Start call
* Accept/reject call
* End call

Prefer WebRTC or another mature real-time media stack rather than implementing media transport manually.

Media must remain end-to-end encrypted.

GUI must display local and remote video.

CLI should allow call control commands but may open a minimal video window for actual video rendering.

Example:

`/call`
`/mute`
`/camera off`
`/hangup`

---

## 5. Screen Sharing

During an active connection/call, a user must be able to share their screen.

Required:

* Start screen sharing
* Stop screen sharing
* Remote user sees shared screen
* Screen stream encrypted end-to-end

For MVP, sharing the entire display is sufficient.

Individual-window/application selection can be added later.

CLI may launch a separate viewer window for received screen/video content.

---

## 6. CLI

Provide a native terminal interface on Windows and Linux.

Minimum commands:

`connect <invite>`
`invite`
`msg <message>`
`history`
`clearhistory`
`call`
`answer`
`reject`
`mute`
`camera on/off`
`share`
`stopshare`
`hangup`
`disconnect`
`quit`
`help`

Normal typed text may optionally send messages directly without requiring `msg`.

---

## 7. Desktop GUI

Provide a simple desktop application using the same underlying communication/core library as the CLI.

Minimum UI:

* Create session
* Join session
* Connection status
* Current chat
* Previous/local conversation history
* Message input
* Send
* Video call button
* Mute
* Camera toggle
* Screen share
* End call
* Clear local history
* Disconnect

Keep UI intentionally simple for MVP.

CLI and GUI should not contain separate networking implementations.

---

## 8. Architecture

Prefer separation similar to:

`Core`

* Session management
* Encryption
* Peer protocol
* Messaging
* Local conversation history
* Media signaling
* Connection state

`Transport`

* cloudflared integration
* P2P/network transport
* WebRTC/signaling where applicable

`Storage`

* Local conversation database/file
* Local preferences

`CLI`

* Terminal interface

`GUI`

* Desktop interface

Both CLI and GUI must use the same Core and Storage layers.

Protocol messages should already include a sender/session identifier even though MVP only supports two users.

This makes future group support easier.

---

## 9. cloudflared

The application should manage `cloudflared` automatically where possible.

The user should not normally need to manually configure Cloudflare Tunnel commands.

Responsibilities include:

* Detecting whether `cloudflared` is installed/bundled
* Starting tunnel when hosting
* Reading generated endpoint information
* Closing tunnel when session ends
* Handling startup failure cleanly

Treat Cloudflare primarily as connectivity infrastructure, not as a trusted message server.

Application-level encryption must remain independent from Cloudflare transport security.

---

## 10. Privacy / Storage

MVP defaults:

* No central database
* No central message storage
* No cloud chat history
* No analytics containing message content
* No mandatory registration
* No plaintext logging of encryption keys, audio, video or screen streams

Allowed local storage:

* Chat history
* Conversation metadata
* Application preferences
* Camera/microphone preferences
* Local user identity/preferences

Local history belongs only to that user's installation and is never automatically uploaded or synchronized.

---

## 11. Future Multi-User Support

Do NOT implement group chat in MVP.

However, avoid assumptions that permanently limit a session to exactly two participant IDs.

Protocol/data structures should support future concepts such as:

* `session_id`
* `conversation_id`
* `participant_id`
* Participant join/leave
* Multiple peer connections
* Group encryption/key management
* Group text chat
* Multi-user video rooms

Optimize the MVP for 1-to-1 simplicity rather than prematurely implementing group infrastructure.

---

## 12. Out of Scope for MVP

Do not implement unless trivial:

* User accounts
* Friend/contact lists
* Offline message delivery through a server
* Server-side history
* Cross-device history synchronization
* File transfers
* Group chat
* Group calls
* Mobile applications
* Message editing/deleting
* Reactions
* Read receipts
* Push notifications
* Cloud backups
* Window-specific screen sharing

---

## 13. MVP Success Criteria

The MVP is complete when two users on Windows and/or Linux can:

1. Launch either CLI or GUI.
2. Create/join a temporary session.
3. Establish an authenticated encrypted connection.
4. Exchange encrypted text and emojis.
5. Close and reopen the application and still see their own locally stored chat history.
6. Start an encrypted audio/video call.
7. Share a screen.
8. Disconnect and terminate temporary networking/session resources.
9. Confirm that Cloudflare/intermediate infrastructure cannot decrypt application content.

CLI ↔ CLI, GUI ↔ GUI and CLI ↔ GUI sessions should all interoperate using the same protocol.

## Developer Priority

Prioritize:

**Reliable connection → encryption/security → text + local history → audio/video → screen sharing → GUI polish.**

Keep dependencies and architecture as simple as possible while using established libraries for cryptography and real-time media.