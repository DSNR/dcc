# Noise XXpsk0 for the Session handshake

The Invite carries only a Rendezvous URL and a Password, so the Peer cannot know the Host's Identity key in advance and IK-style patterns are out; XX is the base. We place the Password PSK at position 0 (`Noise_XXpsk0_25519_ChaChaPoly_BLAKE2s`) rather than the spec-listed `XXpsk3` so that a wrong Password fails at message 1 and the Host never sends its long-term Identity public key to anyone holding only the URL (Cloudflare sees every URL). psk0 is a valid placement under the Noise spec's PSK rules and is supported by `flynn/noise`; it is just not one of the spec's named variants, so do not "fix" it to psk3.

## Consequences

- The Security Code is derived from the two Identity public keys, not the per-Session handshake hash, so it is stable across Sessions.
- Each side's DTLS certificate fingerprint and display name travel in the handshake payloads; all later Signaling is Noise transport, so WebRTC inherits the Password + Identity authentication.
