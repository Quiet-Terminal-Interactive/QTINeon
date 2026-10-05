# Contributing

Each implementation has its own contribution guide covering language-specific requirements, build tooling, and code style. This file covers the general guidelines that apply across all implementations.

## Pull Requests

- Keep PRs focused: one logical change per PR
- All tests must pass
- Update [CHANGELOG.md](CHANGELOG.md) under `[Unreleased]`
- Document all new public API surface in whatever format is idiomatic for that implementation

## Reporting Bugs

Open an issue at [github.com/Quiet-Terminal-Interactive/QTINeon/issues](https://github.com/Quiet-Terminal-Interactive/QTINeon/issues).

Include:
- The implementation and version you are using
- A minimal reproducible test or description of the packet trace
- The config you are using (or "defaults")

## Adding a Packet Type

Any new packet type must be added to all implementations to maintain interoperability. The wire format is defined in [PROTOCOL.md](PROTOCOL.md) — that is the source of truth.

<details>
<summary>Java</summary>

1. Add a constant to `PacketType` with its byte value
2. Add a `record` implementing `PacketPayload` with `toBytes()` and `fromBytes(byte[])`
3. Register the deserializer in `PayloadDeserializer` / `NeonPacket.fromBytes()`
4. Handle it in `NeonRelay.handlePacket()`, `NeonHost.handlePacket()`, or `NeonClient.handlePacket()` as appropriate
5. Add tests

</details>

<details>
<summary>Python</summary>

1. Add a member to `PacketType` in `_protocol.py` with its byte value
2. Add a `@dataclass(frozen=True)` with `to_bytes()` and `from_bytes(data)` class methods
3. Add a `case` branch to `NeonPacket.from_bytes()` for the new type
4. Handle it in `NeonRelay._handle_packet()`, `NeonHost._handle_packet()`, or `NeonClient._handle_packet()` as appropriate
5. Add tests

</details>

<details>
<summary>TypeScript</summary>

1. Add a member to the `PacketType` enum in `src/_protocol.ts`
2. Add a payload interface (e.g. `MyPacketPayload`) and a type guard (`isMyPacket`)
3. Add serialisation in `serializePacket` and deserialisation in `parsePacket`
4. Handle it in `NeonRelay._handlePacket()`, `NeonHost._handlePacket()`, or `NeonClient._handlePacket()` as appropriate
5. Export the new interface and type guard from `src/index.ts`
6. Add tests

</details>

<details>
<summary>Godot</summary>

1. Add a constant to `_protocol.gd` (e.g. `const PT_MY_PACKET := 0x07`)
2. Add `parse_my_packet(p)` and `build_my_packet(...)` static functions to `_protocol.gd`
3. Add a `match` branch to `_handle_raw()` in `NeonRelay.gd`, `_handle_packet()` in `NeonHost.gd`, or `_handle_packet()` in `NeonClient.gd` as appropriate
4. Add tests

</details>

<details>
<summary>Go</summary>

1. Add a constant to `PacketType` in `protocol.go` with its byte value
2. Add a struct with a `MarshalBinary() ([]byte, error)` method and a matching `UnmarshalXxx(data []byte) (Xxx, error)` function
3. Add a case to the type switch in `NeonPacket.ToBytes()` / `NeonPacketFromBytes()`
4. Handle it in `NeonRelay`'s packet dispatch in `relay.go`, `NeonHost` in `host.go`, or `NeonClient` in `client.go` as appropriate
5. Add tests

</details>

## Adding a New Language Implementation

New language implementations are always welcome. To be accepted, the implementation must be integrated into `test_compliance.py` and all compliance tests must pass.

Concretely, for a new implementation called `<lang>`:

1. Add host and client script templates (analogous to `_PY_HOST` / `_PY_CLIENT` or the Java runners) that print the same sentinel markers: `HOST_READY`, `HOST_FAILED`, `CLIENT_CONNECTED:<id>:<name>`, `PACKET_RECEIVED:<type>:<sender>`, `CONNECTED:<id>`, `PACKET_SENT`, `CONNECT_FAILED`.
2. Add a setup step (build, install, create an isolated environment) following the pattern of the Java `mvn install` and Python `venv` steps.
3. Add two test functions:
   - `_test_java_host_<lang>_client()` — Java host, new client (use a new session ID constant)
   - `_test_<lang>_host_java_client()` — new host, Java client (use another new session ID constant)
4. Call both functions from `main()`, appending failures to the `failures` list.
5. Add cleanup of any build artifacts or temporary environments in the cleanup step.
6. Update `CONTRIBUTING.md` with language-specific Requirements, Build, Tests, and Code Style sections.

The Java implementation is the wire-format reference. If the new implementation disagrees with Java on packet framing or session handshake, fix the new implementation — not Java.

## Requirements

<details>
<summary>Go</summary>

- Go 1.22+

No additional dependencies are required for DTLS — `github.com/pion/dtls/v2` is a direct, pure-Go module dependency.

</details>

<details>
<summary>Godot</summary>

- Godot 4.2+ (headless build required for the compliance test runner)
- No additional dependencies — all DTLS uses Godot's built-in `DTLSServer` / `PacketPeerDTLS` / `TLSOptions`

</details>

<details>
<summary>Java</summary>

- Java 25 (OpenJDK 25.0.2+)
- Maven 3.9+

</details>

<details>
<summary>Python</summary>

- Python 3.11+
- pip / a virtual environment

Optional for DTLS:
- `pyopenssl>=23.0` (`pip install qti-neon[dtls]`)

</details>

<details>
<summary>TypeScript</summary>

- Node.js 18+
- npm 9+

Optional for DTLS:
- `koffi` (`npm install koffi`) — requires OpenSSL 3 (`libssl.so.3`) on the system

</details>

## Build

<details>
<summary>Go</summary>

```bash
cd go
go build ./...
go vet ./...
```

Generate docs:

```bash
gomarkdoc ./... --output ../docs/go/README.md
# output: ../docs/go/README.md
```

</details>

<details>
<summary>Godot</summary>

No build step, the implementation is pure GDScript. Copy (or symlink) `godot/addons/qti_neon/` into your project's `addons/` directory and enable the plugin in **Project -> Project Settings -> Plugins**.

To verify the compliance scripts parse correctly:

```bash
cd godot
godot --headless --check-only --script compliance/neon_host_runner.gd
```

</details>

<details>
<summary>Java</summary>

```bash
mvn verify
```

This compiles, runs all tests, generates Javadoc, and enforces code coverage.

</details>

<details>
<summary>Python</summary>

```bash
cd python
pip install -e ".[dev]"
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
npm run build   # tsc → dist/
```

Generate docs:

```bash
npm run docs
# output: ../docs/ts/index.html
```

To install DTLS support:

```bash
npm install koffi
```

</details>

## Tests

<details>
<summary>Go</summary>

Tests open real UDP sockets on loopback:

```bash
cd go
go test ./...
```

Run a specific test file's tests:

```bash
go test ./... -run TestHost
```

Run only the integration tests:

```bash
go test ./... -run TestIntegration
```

Tests are split by concern:

| File                       | What it tests                                              |
| -------------------------- | ----------------------------------------------------------- |
| `protocol_test.go`         | Packet parsing, serialisation, config                       |
| `config_test.go`           | NeonConfig validation                                        |
| `relay_test.go`            | NeonRelay with raw socket counterparts                       |
| `host_test.go`             | NeonHost with a mock relay                                   |
| `client_test.go`           | NeonClient with a mock relay                                  |
| `reliable_test.go`         | ReliablePacketManager in isolation                            |
| `integration_test.go`      | Full stack: relay + host + client over loopback               |
| `dtls_integration_test.go` | Full encrypted stack over loopback with a self-signed cert    |

</details>

<details>
<summary>Godot</summary>

The GDScript implementation is covered by the cross-language compliance test in `test_compliance.py` (tests E and F). There is no standalone unit test suite; protocol correctness is validated by interoperating with the Java reference implementation.

To run the Godot compliance tests:

```bash
python3 test_compliance.py
```

Compliance scripts are in `godot/compliance/` and are run as:

```bash
godot --headless --path godot/ --script compliance/neon_host_runner.gd   -- <session_id> <relay>
godot --headless --path godot/ --script compliance/neon_client_runner.gd -- <session_id> <relay>
```

</details>

<details>
<summary>Java</summary>

Most tests open real UDP sockets on loopback. Run them in a terminal — not in a sandboxed IDE runner:

```bash
mvn test
```

Run a specific test class:

```bash
mvn test -Dtest="NeonHostTest"
```

Run only the integration tests:

```bash
mvn test -Dtest="*Integration*"
```

Tests are split by concern:

| Package       | What it tests                                   |
| ------------- | ----------------------------------------------- |
| `core`        | Protocol parsing, config, buffer pool           |
| `relay`       | NeonRelay with raw socket counterparts          |
| `host`        | NeonHost with a mock relay                      |
| `client`      | NeonClient with a mock relay                    |
| `reliability` | ReliablePacketManager in isolation              |
| `integration` | Full stack: relay + host + client over loopback |

</details>

<details>
<summary>Python</summary>

Most tests open real UDP sockets on loopback. Run them in a terminal — not in a sandboxed IDE runner:

```bash
cd python
pytest
```

Run a specific test file:

```bash
pytest tests/test_host.py
```

Run only the integration tests:

```bash
pytest tests/test_integration.py
```

Tests are split by concern:

| File                  | What it tests                                   |
| --------------------- | ----------------------------------------------- |
| `test_protocol.py`    | Packet parsing, serialisation, config           |
| `test_config.py`      | NeonConfig validation                           |
| `test_relay.py`       | NeonRelay with raw socket counterparts          |
| `test_host.py`        | NeonHost with a mock relay                      |
| `test_client.py`      | NeonClient with a mock relay                    |
| `test_reliable.py`    | ReliablePacketManager in isolation              |
| `test_integration.py` | Full stack: relay + host + client over loopback |

</details>

<details>
<summary>TypeScript</summary>

Tests open real UDP sockets on loopback. Run them in a terminal — not in a sandboxed IDE runner:

```bash
cd js-ts
npm test
```

Run a specific test file:

```bash
npx vitest run tests/host.test.ts
```

Tests are split by concern:

| File                   | What it tests                                   |
| ---------------------- | ----------------------------------------------- |
| `protocol.test.ts`     | Packet parsing, serialisation, wire bytes       |
| `relay.test.ts`        | NeonRelay with raw socket counterparts          |
| `host.test.ts`         | NeonHost with a mock relay                      |
| `client.test.ts`       | NeonClient with a mock relay                    |
| `integration.test.ts`  | Full stack: relay + host + client over loopback |

</details>

## Code Style

<details>
<summary>Go</summary>

- Go 1.22+ — `gofmt` and `go vet` must be clean
- No `fmt.Println`/`fmt.Printf` in the library (use `log/slog`)
- No comments that describe *what* the code does — only *why*, when non-obvious
- No speculative abstractions — solve the problem in front of you
- Unexported (lowercase) identifiers for internals; exported (uppercase) only for the API surface
- One `*slog.Logger` per component (e.g. `relayLogger`, `hostLogger`, `clientLogger`)
- 64-bit wire fields (`token`, `hostToken`, `timestamp`) use native `int64`/`uint64`; no special handling needed

</details>

<details>
<summary>Godot</summary>

- Godot 4.2+ GDScript with static typing where practical
- No `print()` in `addons/qti_neon/` (use `push_error()` / `push_warning()` for runtime diagnostics)
- No comments that describe *what* the code does — only *why*, when non-obvious
- No speculative abstractions — solve the problem in front of you
- Prefix internal scripts with `_` (e.g. `_protocol.gd`, `_socket.gd`); `class_name` only for public API scripts
- Use `Mutex` for all state shared between the processing thread and the application thread
- All 64-bit wire fields (`token`, `host_token`, `timestamp`) use GDScript's native `int` (64-bit signed); no special handling needed

</details>

<details>
<summary>Java</summary>

- Java 25 — use records, sealed interfaces, pattern matching, and virtual threads where natural
- No `System.out.println` in `src/main/`
- No comments that describe *what* the code does — only *why*, when non-obvious
- No speculative abstractions — solve the problem in front of you
- Package-private for implementation classes; `public` only for the API surface
- One `Logger` per class via `Logger.getLogger(Foo.class.getName())`

</details>

<details>
<summary>Python</summary>

- Python 3.11+ — use `match`/`case`, `dataclass(frozen=True)`, and `|` union types where natural
- No `print()` in `src/`
- No comments that describe *what* the code does — only *why*, when non-obvious
- No speculative abstractions — solve the problem in front of you
- Prefix internal classes and functions with `_`; public API only in `__init__.py`
- One `logger = logging.getLogger(__name__)` per module

</details>

<details>
<summary>TypeScript</summary>

- TypeScript strict mode; `ES2022` target, CommonJS output
- No `console.log` in `src/` (use `emit('error', ...)` for runtime errors)
- No comments that describe *what* the code does — only *why*, when non-obvious
- No speculative abstractions — solve the problem in front of you
- Prefix internal modules with `_` (e.g. `_protocol.ts`, `_socket.ts`); public API only in `index.ts`
- Use `bigint` for all wire-format 64-bit integer fields (token, timestamp); never `number`
- Async entry points (`start()`, `connect()`) return `Promise`; the run loop is driven by `setInterval` with `.unref()` so the process does not stay alive indefinitely

</details>