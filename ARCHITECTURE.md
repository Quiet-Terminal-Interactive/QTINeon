# Architecture

## Topology

```
                    ┌──────────────────────────────────┐
                    │             Relay                │
                    │                                  │
                    │  SessionManager                  │
                    │    session 42:                   │
                    │      host → addr:port            │
                    │      client 2 → addr:port        │
                    │      client 3 → addr:port        │
                    │                                  │
                    │  RateLimiter (per source)        │
                    │  PendingConnection queue         │
                    │  PendingReconnect map            │
                    └──────────────┬───────────────────┘
                                   │ UDP
               ┌───────────────────┼───────────────────┐
               │                   │                   │
        ┌──────┴──────┐    ┌───────┴──────┐     ┌──────┴──────┐
        │   Client    │    │    Client    │     │    Host     │
        │  (client 2) │    │  (client 3)  │     │   (id=1)    │
        └─────────────┘    └──────────────┘     └─────────────┘
```

All UDP traffic flows through the relay. Clients and the host never communicate directly, which means:

- NAT traversal is handled entirely by the relay's single public address
- The host's real address is never exposed to clients
- Clients connect to the relay address, not each other

## Component Responsibilities

### Relay

- Routes packets by destination ID in the header
- Manages the host-registration and connection handshakes
- Enforces per-source rate limits
- Evicts stale connections on a cleanup interval
- Buffers reconnect requests until the host validates them

The relay is stateless with respect to game logic. It parses the packet header plus the payloads of lifecycle packets. `HOST_REGISTER`, `CONNECT_REQUEST`, `CONNECT_ACCEPT`, `RECONNECT_REQUEST`, and `DISCONNECT_NOTICE` have relay-specific handlers; all other valid packets are routed opaquely by destination ID.

Applications can extend the relay with a packet hook (`onPacket` / `on_packet` / `_on_packet` in subclasses, `SetPacketHandler` in Go) that runs after rate limiting and before the default handlers. Returning `true` consumes the packet; the default hook returns `false`, so an unextended relay behaves exactly as described above.

### Host

- Registers a session with the relay
- Assigns client IDs (starts at 2; ID 1 is the host itself)
- Generates a cryptographically random session token per client for reconnect
- Sends `SESSION_CONFIG` reliably
- Maintains connected/disconnected client state
- Dispatches game packets to the application

### Client

- Sends `CONNECT_REQUEST` and waits for `CONNECT_ACCEPT`
- Stores session token and client ID for reconnect
- Drives the packet loop
- Sends auto-pings at a configurable interval
- Recreates its UDP socket on reconnect if the original is closed

## Session Lifecycle

```
Host registers
      │
      ▼
[REGISTERED] ─── client joins ──► [ACTIVE: N clients]
                                          │
                               client disconnects
                                          │
                                          ▼
                                 [token retained, N-1 clients]
                                          │
                              reconnect window expires
                                          │
                                          ▼
                                 [slot freed permanently]
```

The reconnect token window is 5 minutes by default. Within that window a client can rejoin with its original ID. After expiry, the host removes the disconnected-client reconnect state and denies that reconnect attempt; normal client ID allocation continues from a monotonic counter.

## Connection Handshake

```
1.  Client sends CONNECT_REQUEST to relay
2.  Relay looks up session, finds host address, adds client to FIFO queue
3.  Relay forwards CONNECT_REQUEST to host
4.  Host reserves the client name atomically
5.  Host allocates a client ID atomically
6.  Host sends CONNECT_ACCEPT, SESSION_CONFIG (reliable), PACKET_TYPE_REGISTRY
7.  Relay pops FIFO queue → maps clientId→clientAddr in SessionManager
8.  Relay forwards CONNECT_ACCEPT to client
9.  Client transitions to RUNNING
10. SESSION_CONFIG and PACKET_TYPE_REGISTRY arrive and are processed
```

## Reconnect Handshake

The critical invariant: **the relay does not update the peer address until the host sends CONNECT_ACCEPT**.

```
1.  Client sends RECONNECT_REQUEST to relay (carries token + old clientId)
2.  Relay stores pending reconnect keyed by "sessionId:clientId", retaining new address
3.  Relay forwards RECONNECT_REQUEST to host using the old address still in SessionManager
4.  Host validates token — if invalid, sends CONNECT_DENY
5.  CONNECT_DENY is routed normally; the pending reconnect entry is not applied
6.  If valid, host sends CONNECT_ACCEPT
7.  Relay finds pending reconnect → updates peer address in SessionManager
8.  Relay forwards CONNECT_ACCEPT to new address
9.  Client receives CONNECT_ACCEPT, stores new token
```

If the host denies the reconnect, the relay leaves the pending entry in place until cleanup expires it.

## DTLS Encryption

DTLS is relay-terminated: each peer maintains a separate DTLS session with the relay. Peers never negotiate DTLS with each other.

```
Client ──── DTLS ────► Relay ◄──── DTLS ──── Host
```

When DTLS is enabled, the host and client perform a client-side handshake with the relay before sending any Neon packets. The relay handles inbound handshakes automatically. After the handshake, encryption and decryption are transparent to game code.

## Packet Processing Loop

Each component runs a processing loop on a dedicated thread or task:

```
while running:
    receive all buffered packets → handle each
    check pending ACKs          (host)
    check cleanup intervals     (relay)
    check auto-ping             (client)
    sleep
```

See [PROTOCOL.md](PROTOCOL.md) for the wire format all implementations must conform to.

For implementation-specific detail — threading model, class structure, DTLS internals — see the architecture doc in the relevant subdirectory.