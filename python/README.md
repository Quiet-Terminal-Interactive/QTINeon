# QTI Neon

Minimal, game-agnostic, relay-based UDP multiplayer protocol library.

```
Client A ←──── UDP ────→ Relay ←──── UDP ────→ Host
Client B ←──── UDP ────→ Relay
```

Clients never communicate directly. The relay routes packets by destination ID in the packet header, keeping NAT traversal trivial and host addresses private. The host is just another participant — it has no special network position, only a special protocol role.

## Features

- Relay-mediated UDP with automatic NAT traversal
- Connection handshake with host-assigned client IDs and session tokens
- Token-based reconnection (5-minute window by default)
- Opt-in reliable delivery with retransmit and duplicate detection
- Per-source rate limiting (100 pps default)
- Auto-ping keepalive
- Optional DTLS 1.2/1.3 encryption — relay-terminated, transparent to game code
- Zero game-specific logic in the relay or library

## Implementations

Each language implementation lives in its own subdirectory at the repository root, alongside its generated documentation under `docs/<language>/`.

All implementations conform to the same protocol spec — a client or host written in any language is fully interoperable with a relay or peer written in any other.

See [PROTOCOL.md](PROTOCOL.md) for the wire format specification that all implementations share.

## Documentation

See [ARCHITECTURE.md](ARCHITECTURE.md) for a full description of the relay topology, session lifecycle, and reconnect flow.

See [PROTOCOL.md](PROTOCOL.md) for the wire format specification.

See [CONFIGURATION.md](CONFIGURATION.md) for the complete configuration reference.

API documentation for each implementation is generated from source and lives under `docs/<language>/`.

## Quick Start

### 1. Run a relay

<details>
<summary>Java</summary>

```java
NeonConfig cfg = NeonConfig.defaults(); // relay port 7777
NeonRelay relay = new NeonRelay("0.0.0.0", cfg);
Thread.ofVirtual().start(relay::startAndRun);
```

</details>

<details>
<summary>Python</summary>

```python
import threading
from qti_neon import NeonRelay, NeonConfig

relay = NeonRelay("0.0.0.0", NeonConfig())
threading.Thread(target=relay.start_and_run, daemon=True).start()
```

</details>

<details>
<summary>TypeScript</summary>

```typescript
import { NeonRelay } from 'qti-neon';

const relay = new NeonRelay('0.0.0.0'); // relay port 7777
await relay.start();
```

</details>

<details>
<summary>Godot</summary>

```gdscript
var relay := NeonRelay.new("0.0.0.0")  # relay port 7777
var thread := Thread.new()
thread.start(relay.start_and_run)
```

</details>

<details>
<summary>Go</summary>

```go
cfg := qtineon.DefaultConfig() // relay port 7777
relay, _ := qtineon.NewRelay("0.0.0.0", cfg)
go relay.StartAndRun()
```

</details>

### 2. Start a host

<details>
<summary>Java</summary>

```java
NeonHost host = new NeonHost(42, "relay.example.com:7777", cfg);
host.setClientConnectCallback((id, name, sid) ->
    System.out.println(name + " joined as " + (id & 0xFF)));
host.setUnhandledPacketCallback((type, from, payload) ->
    handleGamePacket(type, from));
Thread.ofVirtual().start(() -> host.startAndRun());
```

</details>

<details>
<summary>Python</summary>

```python
import threading
from qti_neon import NeonHost

host = NeonHost(session_id=42, relay_address="relay.example.com:7777")
host.set_client_connect_callback(lambda cid, name, sid: on_client_join(cid, name))
host.set_unhandled_packet_callback(lambda ptype, sender, payload: handle_game_packet(ptype, sender))
threading.Thread(target=host.start_and_run, daemon=True).start()
```

</details>

<details>
<summary>TypeScript</summary>

```typescript
import { NeonHost } from 'qti-neon';

const host = new NeonHost(42, 'relay.example.com:7777');
host.setClientConnectCallback((id, name, sid) => onClientJoin(id, name));
host.setUnhandledPacketCallback((type, from, payload) => handleGamePacket(type, from));
await host.start();
```

</details>

<details>
<summary>Godot</summary>

```gdscript
var host := NeonHost.new(42, "relay.example.com:7777")
host.set_client_connect_callback(func(id, name, sid): on_client_join(id, name))
host.set_unhandled_packet_callback(func(type, from, payload): handle_game_packet(type, from))
var thread := Thread.new()
thread.start(host.start_and_run)
```

</details>

<details>
<summary>Go</summary>

```go
host, _ := qtineon.NewHost(42, "relay.example.com:7777", cfg)
host.SetClientConnectCallback(func(id uint8, name string, sid int32) {
    fmt.Println(name, "joined as", id)
})
host.SetUnhandledPacketCallback(func(ptype, from uint8, payload []byte) {
    handleGamePacket(ptype, from)
})
go host.StartAndRun()
```

</details>

### 3. Connect a client

<details>
<summary>Java</summary>

```java
NeonClient client = new NeonClient("player1", cfg);
client.setSessionConfigCallback(sc ->
    System.out.println("Tick rate: " + sc.tickRate()));
client.setUnhandledPacketCallback((type, from, payload) ->
    handleGamePacket(type, from));

if (client.connect(42, "relay.example.com:7777")) {
    Thread.ofVirtual().start(client::run);
}
```

</details>

<details>
<summary>Python</summary>

```python
import threading
from qti_neon import NeonClient

client = NeonClient("player1")
client.set_session_config_callback(lambda sc: on_session_config(sc))
client.set_unhandled_packet_callback(lambda ptype, sender, payload: handle_game_packet(ptype, sender))

if client.connect(session_id=42, relay_address="relay.example.com:7777"):
    threading.Thread(target=client.run, daemon=True).start()
```

</details>

<details>
<summary>TypeScript</summary>

```typescript
import { NeonClient } from 'qti-neon';

const client = new NeonClient('player1');
client.setSessionConfigCallback(sc => onSessionConfig(sc));
client.setUnhandledPacketCallback((type, from, payload) => handleGamePacket(type, from));

await client.connect(42, 'relay.example.com:7777');
```

</details>

<details>
<summary>Godot</summary>

```gdscript
var client := NeonClient.new("player1")
client.set_unhandled_packet_callback(func(type, from, payload): handle_game_packet(type, from))

if client.connect(42, "relay.example.com:7777"):
    var thread := Thread.new()
    thread.start(client.run)
```

</details>

<details>
<summary>Go</summary>

```go
client, _ := qtineon.NewClient("player1", cfg)
client.SetSessionConfigCallback(func(sc qtineon.SessionConfig) {
    fmt.Println("Tick rate:", sc.TickRate)
})
client.SetUnhandledPacketCallback(func(ptype, from uint8, payload []byte) {
    handleGamePacket(ptype, from)
})

if client.Connect(42, "relay.example.com:7777") {
    go client.Run()
}
```

</details>

### 4. Send a packet

<details>
<summary>Java</summary>

```java
byte[] data = encodePosition(x, y, z);
client.sendPacket(data, PACKET_POSITION, (byte) 0); // 0 = broadcast
```

</details>

<details>
<summary>Python</summary>

```python
data = encode_position(x, y, z)
client.send_packet(data, PACKET_POSITION, dest_id=0)  # 0 = broadcast
```

</details>

<details>
<summary>TypeScript</summary>

```typescript
const data = encodePosition(x, y, z);
client.sendPacket(data, PACKET_POSITION, 0); // 0 = broadcast
```

</details>

<details>
<summary>Godot</summary>

```gdscript
var data := encode_position(x, y, z)
client.send_packet(data, PACKET_POSITION, 0)  # 0 = broadcast
```

</details>

<details>
<summary>Go</summary>

```go
data := encodePosition(x, y, z)
client.SendPacket(data, PacketPosition, 0) // 0 = broadcast
```

</details>

### 5. Reconnect after drop

<details>
<summary>Java</summary>

```java
client.stop(); // or socket dies ungracefully
// ...
boolean ok = client.reconnect(); // uses stored session token
```

</details>

<details>
<summary>Python</summary>

```python
client.stop()  # or socket dies ungracefully
# ...
ok = client.reconnect()  # uses stored session token
```

</details>

<details>
<summary>TypeScript</summary>

```typescript
client.stop(); // or socket dies ungracefully
// ...
const ok = await client.reconnect(); // uses stored session token
```

</details>

<details>
<summary>Godot</summary>

```gdscript
client.stop()  # or socket dies ungracefully
# ...
var ok := client.reconnect()  # uses stored session token
```

</details>

<details>
<summary>Go</summary>

```go
client.Stop() // or socket dies ungracefully
// ...
ok := client.Reconnect(5) // uses stored session token, up to 5 attempts
```

</details>

## DTLS Encryption

DTLS is opt-in — the relay, host, and client handle the handshake automatically before any Neon packets are exchanged.

<details>
<summary>Java</summary>

```java
// Relay — load a PKCS12 keystore with the relay's certificate
KeyStore ks = KeyStore.getInstance("PKCS12");
try (var is = new FileInputStream("relay.p12")) {
    ks.load(is, "password".toCharArray());
}
SSLContext relayCtx = DtlsConfig.fromKeyStore(ks, "password".toCharArray());

NeonConfig relayCfg = NeonConfig.builder()
    .sslContext(relayCtx)
    .build();

// Host / Client — trust the relay certificate (or use a proper TrustManager)
SSLContext clientCtx = DtlsConfig.insecureTrustAll(); // dev only
NeonConfig clientCfg = NeonConfig.builder()
    .sslContext(clientCtx)
    .build();
```

</details>

<details>
<summary>Python</summary>

```python
from qti_neon import DtlsConfig, NeonConfig, NeonRelay, NeonClient

# Relay — load certificate and private key
relay_cfg = NeonConfig(dtls_config=DtlsConfig.from_key_store("relay.crt", "relay.key"))
relay = NeonRelay("0.0.0.0", relay_cfg)

# Host / Client — trust the relay certificate (or supply a proper trust store)
client_cfg = NeonConfig(dtls_config=DtlsConfig.insecure_trust_all())  # dev only
client = NeonClient("player1", client_cfg)
```

</details>

<details>
<summary>TypeScript</summary>

```typescript
import { DtlsConfig, NeonConfig, NeonRelay, NeonClient } from 'qti-neon';

// Relay — load certificate and private key
const relayCfg = new NeonConfig({ dtlsConfig: DtlsConfig.fromKeyStore('relay.crt', 'relay.key') });
const relay = new NeonRelay('0.0.0.0', relayCfg);

// Host / Client — trust the relay certificate (or supply a proper trust store)
const clientCfg = new NeonConfig({ dtlsConfig: DtlsConfig.insecureTrustAll() }); // dev only
const client = new NeonClient('player1', clientCfg);
```

</details>

<details>
<summary>Godot</summary>

```gdscript
# Relay — load certificate and private key (PEM files)
var relay_cfg := NeonConfig.new({dtls_config = DtlsConfig.from_key_store("relay.crt", "relay.key")})
var relay := NeonRelay.new("0.0.0.0", relay_cfg)

# Host / Client — trust the relay certificate (or use insecure for dev)
var client_cfg := NeonConfig.new({dtls_config = DtlsConfig.insecure_trust_all()})  # dev only
var client := NeonClient.new("player1", client_cfg)
```

</details>

<details>
<summary>Go</summary>

```go
// Relay — load a PEM certificate and private key
relayCfg := qtineon.DefaultConfig()
relayCfg.DTLS, _ = qtineon.FromKeyStore("relay.pem", "relay-key.pem")
relay, _ := qtineon.NewRelay("0.0.0.0", relayCfg)

// Host / Client — trust the relay certificate (or supply a proper trust store)
clientCfg := qtineon.DefaultConfig()
clientCfg.DTLS = qtineon.InsecureTrustAll() // dev only
client, _ := qtineon.NewClient("player1", clientCfg)
```

</details>

`insecure_trust_all()` / `insecureTrustAll()` / `InsecureTrustAll()` is for development and testing only — it accepts any certificate.
For production, supply a trust manager that pins the relay's certificate.

When DTLS is not configured (the default), packets are sent in plaintext.

## Installation

<details>
<summary>Java (Maven)</summary>

```xml
<dependency>
    <groupId>com.quietterminal</groupId>
    <artifactId>qti-neon</artifactId>
    <version>1.0.0</version>
</dependency>
```

</details>

<details>
<summary>Python (pip)</summary>

```bash
pip install qti-neon
```

With DTLS support:

```bash
pip install "qti-neon[dtls]"
```

</details>

<details>
<summary>TypeScript (npm)</summary>

```bash
npm install qti-neon
```

With DTLS support:

```bash
npm install koffi
```

</details>

<details>
<summary>Godot (Asset Library / manual)</summary>

Copy (or symlink) `godot/addons/qti_neon/` into your project's `addons/` directory, then enable the plugin in **Project → Project Settings → Plugins**.

No package manager step is required — the implementation is pure GDScript with no external dependencies.

</details>

<details>
<summary>Go (go get)</summary>

```bash
go get github.com/Quiet-Terminal-Interactive/QTINeon/go
```

DTLS support (`github.com/pion/dtls/v2`) is a direct dependency and requires no extra installation step.

</details>

## Building

<details>
<summary>Java</summary>

```bash
mvn verify
```

Tests that open UDP sockets (most of them) must run outside a sandbox:

```bash
mvn test
```

Generate docs:

```bash
mvn javadoc:javadoc
# output: target/reports/apidocs/index.html
```

</details>

<details>
<summary>Python</summary>

```bash
cd python
pip install -e ".[dev]"
pytest
```

Generate docs:

```bash
pdoc src/qti_neon --output-dir ../docs/python
# output: ../docs/python/qti_neon.html
```

</details>

<details>
<summary>TypeScript</summary>

```bash
cd js-ts
npm install
npm test
```

Generate docs:

```bash
npm run docs
# output: ../docs/ts/index.html
```

</details>

<details>
<summary>Godot</summary>

No build step — the implementation is pure GDScript. To verify the compliance scripts parse correctly:

```bash
cd godot
godot --headless --check-only --script compliance/neon_host_runner.gd
```

To run the cross-language compliance tests (requires Java and Node.js):

```bash
python3 test_compliance.py
```

</details>

<details>
<summary>Go</summary>

```bash
cd go
go build ./...
go vet ./...
```

Tests open real UDP sockets on loopback:

```bash
go test ./...
```

Generate docs:

```bash
gomarkdoc ./... --output ../docs/go/README.md
# output: ../docs/go/README.md
```

</details>

## Client IDs

| ID      | Role                   |
| ------- | ---------------------- |
| `0`     | Broadcast / unassigned |
| `1`     | Host                   |
| `2–254` | Connected clients      |
| `255`   | Reserved               |

## License

MIT — see [LICENSE](LICENSE).
