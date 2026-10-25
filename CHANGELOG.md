# Changelog

All notable changes to QTI Neon are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/).

## [1.2.0] — 2026-10-25

### Added
- `NeonRelay` packet hook for custom relay handling, called for every packet that passes rate limiting before default handling; return `true` to consume the packet. The default hook does nothing, so existing relays are unaffected.
  - Java: override `protected boolean onPacket(NeonPacket, SocketAddress)` (`NeonRelay` is no longer `final`)
  - Python: override `on_packet(packet, source)`
  - TypeScript: override `protected onPacket(packet, source)`
  - Godot: override `_on_packet(hdr, data, peer_key)` in a script that `extends NeonRelay`
  - Go: `NeonRelay.SetPacketHandler(func(NeonPacket, net.UDPAddr) bool)`
- `NeonRelay` send helper for replying from the hook: `send` (Java, Python, TypeScript, Godot) and `Send` (Go)

## [1.0.0] — 2026-05-26

### Added
- `NeonRelay` — relay server with session management, FIFO connection queuing, and rate limiting
- `NeonHost` — session host with atomic client ID assignment, reliable `SESSION_CONFIG` delivery, and token-based reconnect
- `NeonClient` — relay-connected client with auto-ping, automatic socket recreation on reconnect, and game loop integration
- `NeonConfig` / `NeonConfig.Builder` — immutable, fully documented configuration with sensible defaults
- `AckStateMachine` — reliable delivery tracking for `SESSION_CONFIG` retransmit
- `ReliablePacketManager` — opt-in reliable delivery for game packets with correct 16-bit wrap-around duplicate detection
- `GamePacketRegistry` / `GamePacketDescriptor` — registration and advertisement of application-defined packet types
- `PacketPayload.PacketTypeRegistry` — host-to-client packet type manifest
- `AbstractLifecycle` — thread-safe CAS-based lifecycle state machine
- `ByteBufferPool` — bounded pool of reusable `ByteBuffer`s
- `NeonSocket` — NIO `DatagramChannel` wrapper with virtual-thread-safe blocking semantics via `Selector`
- `SessionState` enum — client-side session state for game code
- `HOST_REGISTER` packet type (`0x06`) — explicit host registration replacing the old clientId=1 hack
- Integration tests — full relay + host + client stack over loopback UDP
- DTLS 1.2/1.3 encryption — relay-terminated, opt-in via `NeonConfig.sslContext(SSLContext)`
- `DtlsSession` — per-peer `SSLEngine` wrapper handling the full DTLS handshake state machine including `NEED_UNWRAP_AGAIN`
- `DtlsConfig` — factory for relay server contexts (`fromKeyStore`) and development client contexts (`insecureTrustAll`)
- DTLS integration tests — full encrypted stack over loopback with a self-signed EC certificate

### Fixed
- Relay no longer updates peer address before host validates a reconnect token
- Name reservation uses `ConcurrentHashMap.putIfAbsent` to eliminate TOCTOU race
- Client socket field is non-final, allowing socket recreation without requiring it to be closed first before reconnect
- Duplicate detection uses `(short)(sequence - lastSeq) <= 0` for correct 16-bit wrap-around
- `SESSION_CONFIG` ACK uses an `AtomicInteger` sequence counter instead of hardcoded `0`
- Relay FIFO connection queue (`pendingBySession`) eliminates race when multiple clients connect to the same session simultaneously
