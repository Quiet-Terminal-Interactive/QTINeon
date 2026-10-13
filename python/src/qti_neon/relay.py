"""Neon relay server."""

from __future__ import annotations

import enum
import logging
import time
import threading
from collections import deque
from dataclasses import dataclass

from ._config import NeonConfig
from ._protocol import (
    NeonPacket,
    PacketType,
    PacketHeader,
    ConnectAccept,
    ConnectDeny,
    DisconnectNotice,
)
from ._rate_limiter import _RateLimiter
from ._session import _SessionManager
from ._socket import _NeonSocket

logger = logging.getLogger(__name__)


@dataclass
class _PendingConnection:
    session_id: int
    name: str
    requested_at: float


@dataclass
class _PendingReconnect:
    session_id: int
    client_id: int
    new_address: tuple[str, int]
    requested_at: float


class _State(enum.Enum):
    CREATED = "CREATED"
    RUNNING = "RUNNING"
    STOPPED = "STOPPED"
    FAILED = "FAILED"


class NeonRelay:
    """UDP relay server for the Neon multiplayer protocol.

    Routes packets between session hosts and clients. The relay is stateless
    with respect to game logic — it only understands protocol lifecycle packets
    and routes everything else by destination ID.

    Typical usage:

    ```python
    import threading
    from qti_neon import NeonRelay, NeonConfig

    relay = NeonRelay("0.0.0.0", NeonConfig())
    thread = threading.Thread(target=relay.start_and_run, daemon=True)
    thread.start()

    # … later …
    relay.stop()
    ```
    """

    def __init__(self, bind_address: str, config: NeonConfig | None = None) -> None:
        """Create a relay bound to *bind_address* on ``config.relay_port``.

        Args:
            bind_address: IP address to bind (e.g. ``"0.0.0.0"`` for all interfaces).
            config: Protocol configuration. Uses defaults when ``None``.

        Raises:
            OSError: if the socket cannot be bound.
        """
        self._config = config or NeonConfig()
        self._socket = _NeonSocket(
            bind_address=(bind_address, self._config.relay_port),
            config=self._config,
        )
        self._session_manager = _SessionManager()
        self._rate_limiters: dict[tuple[str, int], _RateLimiter] = {}
        # pending_connections and pending_by_session are only touched from the
        # relay processing thread — no locking required.
        self._pending_connections: dict[tuple[str, int], _PendingConnection] = {}
        self._pending_by_session: dict[int, deque[tuple[str, int]]] = {}
        self._pending_reconnects: dict[str, _PendingReconnect] = {}
        self._last_cleanup: float = time.monotonic()
        self._state = _State.CREATED
        self._state_lock = threading.Lock()

    # ------------------------------------------------------------------ #
    # Lifecycle                                                            #
    # ------------------------------------------------------------------ #

    def start(self) -> None:
        """Initialise the relay without entering the processing loop.

        Call `start_and_run` instead for typical use.

        Raises:
            RuntimeError: if called from a state other than ``CREATED``.
        """
        with self._state_lock:
            if self._state is not _State.CREATED:
                raise RuntimeError(f"Cannot start from state: {self._state.value}")
            self._state = _State.RUNNING
        if self._config.is_dtls_enabled:
            self._socket.enable_server_dtls(self._config.dtls_config)  # type: ignore[arg-type]
        logger.info("NeonRelay started on port %d", self._config.relay_port)

    def stop(self) -> None:
        """Signal the relay to stop and close the socket.

        Raises:
            RuntimeError: if the relay is not running.
        """
        with self._state_lock:
            if self._state is not _State.RUNNING:
                raise RuntimeError(f"Cannot stop from state: {self._state.value}")
            self._state = _State.STOPPED
        self._socket.close()

    def start_and_run(self) -> None:
        """Start the relay and block in the processing loop until `stop` is called.

        Intended to run in a dedicated thread:

        ```python
        threading.Thread(target=relay.start_and_run, daemon=True).start()
        ```

        Raises:
            OSError: if socket initialisation fails.
        """
        self.start()
        self._run()

    def _run(self) -> None:
        while self._state is _State.RUNNING:
            try:
                self._process_packets()
                self._perform_cleanup()
                time.sleep(self._config.relay_main_loop_sleep_ms / 1000.0)
            except InterruptedError:
                break
            except OSError as exc:
                if not self._socket.is_closed:
                    logger.warning("IO error in relay loop: %s", exc)
                break

    @property
    def is_running(self) -> bool:
        """``True`` while the relay processing loop is active."""
        return self._state is _State.RUNNING

    def local_address(self) -> tuple[str, int]:
        """Return the local ``(host, port)`` the relay is bound to."""
        return self._socket.local_address()

    def __enter__(self) -> "NeonRelay":
        return self

    def __exit__(self, *_: object) -> None:
        if self._state is _State.RUNNING:
            self.stop()

    # ------------------------------------------------------------------ #
    # Packet processing                                                    #
    # ------------------------------------------------------------------ #

    def _process_packets(self) -> None:
        while True:
            result = self._socket.receive_packet()
            if result is None:
                break
            packet, source = result
            self._session_manager.update_last_seen(source)

            limiter = self._rate_limiters.get(source)
            if limiter is None:
                if len(self._rate_limiters) >= self._config.max_rate_limiters:
                    logger.debug("Rate limiter capacity reached; packet discarded from %s", source)
                    continue
                limiter = _RateLimiter(self._config.max_packets_per_second)
                self._rate_limiters[source] = limiter
            if not limiter.allow_packet():
                logger.debug("Rate limited: %s", source)
                continue

            self._handle_packet(packet, source)

    def _handle_packet(self, packet: NeonPacket, source: tuple[str, int]) -> None:
        payload = packet.payload
        match payload:
            case _HostRegister():
                self._handle_host_register(payload, source)
            case _ConnectRequest():
                self._handle_connect_request(payload, source, packet.header)
            case ConnectAccept():
                self._handle_connect_accept(payload, source, packet.header)
            case _ReconnectRequest():
                self._handle_reconnect_request(payload, source)
            case DisconnectNotice():
                self._handle_disconnect_notice(source, packet.header)
            case _:
                self._route_packet(packet, source)

    def _handle_host_register(
        self, req: "_HostRegister", source: tuple[str, int]
    ) -> None:
        if req.session_id <= 0:
            logger.warning("Rejected HOST_REGISTER: invalid session_id=%d", req.session_id)
            return
        self._session_manager.register_host(req.session_id, source)
        logger.info("Host registered for session %d from %s", req.session_id, source)
        accept = ConnectAccept(
            client_id=1,
            session_id=req.session_id,
            token=req.host_token,
        )
        self._socket.send_packet(
            NeonPacket.create(PacketType.CONNECT_ACCEPT, 0, 0, 1, accept),
            source,
        )

    def _handle_connect_request(
        self,
        req: "_ConnectRequest",
        source: tuple[str, int],
        header: PacketHeader,
    ) -> None:
        host = self._session_manager.get_host(req.session_id)
        if host is None:
            self._send_deny(source, "Session not found")
            return
        if self._session_manager.get_client_count(req.session_id) >= self._config.max_clients_per_session:
            self._send_deny(source, "Session full")
            return
        if len(self._pending_connections) >= self._config.max_pending_connections:
            self._send_deny(source, "Relay busy")
            return

        self._pending_connections[source] = _PendingConnection(
            session_id=req.session_id,
            name=req.name,
            requested_at=time.monotonic(),
        )
        queue = self._pending_by_session.setdefault(req.session_id, deque())
        queue.append(source)

        self._socket.send_packet(
            NeonPacket.create(
                PacketType.CONNECT_REQUEST,
                header.sequence,
                header.client_id,
                1,
                req,
            ),
            host,
        )

    def _handle_connect_accept(
        self,
        accept: ConnectAccept,
        source: tuple[str, int],
        header: PacketHeader,
    ) -> None:
        client_id = accept.client_id
        session_id = accept.session_id

        if client_id == 1:
            # Relay acknowledges its own host registration — ignore.
            logger.debug("Ignoring CONNECT_ACCEPT(client_id=1) from %s", source)
            return

        reconnect_key = f"{session_id}:{client_id}"
        reconnect = self._pending_reconnects.pop(reconnect_key, None)
        if reconnect is not None:
            self._session_manager.update_peer_address(
                session_id, client_id, reconnect.new_address
            )
            self._socket.send_packet(
                NeonPacket.create(
                    PacketType.CONNECT_ACCEPT,
                    header.sequence,
                    header.client_id,
                    client_id,
                    accept,
                ),
                reconnect.new_address,
            )
            logger.info(
                "Reconnect confirmed: client %d session %d", client_id, session_id
            )
            return

        queue = self._pending_by_session.get(session_id)
        client_addr = queue.popleft() if queue else None
        if client_addr is None:
            logger.warning(
                "CONNECT_ACCEPT for session %d but no pending clients", session_id
            )
            return
        if not queue:
            self._pending_by_session.pop(session_id, None)

        self._pending_connections.pop(client_addr, None)
        self._session_manager.register_peer(session_id, client_id, client_addr)
        self._socket.send_packet(
            NeonPacket.create(
                PacketType.CONNECT_ACCEPT,
                header.sequence,
                header.client_id,
                client_id,
                accept,
            ),
            client_addr,
        )
        logger.info("Client %d joined session %d", client_id, session_id)

    def _handle_reconnect_request(
        self, req: "_ReconnectRequest", source: tuple[str, int]
    ) -> None:
        host = self._session_manager.get_host(req.session_id)
        if host is None:
            self._send_deny(source, "Session not found")
            return
        key = f"{req.session_id}:{req.previous_client_id}"
        new_reconnect = key not in self._pending_reconnects
        if new_reconnect and len(self._pending_reconnects) >= self._config.max_pending_connections:
            self._send_deny(source, "Relay busy")
            return
        self._pending_reconnects[key] = _PendingReconnect(
            session_id=req.session_id,
            client_id=req.previous_client_id,
            new_address=source,
            requested_at=time.monotonic(),
        )
        if new_reconnect:
            self._socket.send_packet(
                NeonPacket.create(
                    PacketType.RECONNECT_REQUEST,
                    0,
                    req.previous_client_id,
                    1,
                    req,
                ),
                host,
            )

    def _handle_disconnect_notice(
        self, source: tuple[str, int], header: PacketHeader
    ) -> None:
        session_id = self._session_manager.get_session_for_peer(source)
        if session_id is not None:
            notice_bytes = NeonPacket.create(
                PacketType.DISCONNECT_NOTICE,
                header.sequence,
                header.client_id,
                0,
                DisconnectNotice(),
            ).to_bytes()
            for peer in self._session_manager.get_all_peers_except(session_id, source):
                self._socket.send(notice_bytes, peer)

        self._session_manager.remove_peer(source)
        self._pending_connections.pop(source, None)
        self._rate_limiters.pop(source, None)
        self._socket.remove_dtls_session(source)

    def _route_packet(self, packet: NeonPacket, source: tuple[str, int]) -> None:
        session_id = self._session_manager.get_session_for_peer(source)
        if session_id is None:
            logger.debug("Unroutable from %s: not in any session", source)
            return

        dest_id = packet.header.destination_id
        raw = packet.to_bytes()

        if dest_id == 0:
            peers = self._session_manager.get_all_peers_except(session_id, source)
            if not peers:
                logger.debug("Unroutable from %s: no peers to broadcast to", source)
                return
            for peer in peers:
                self._socket.send(raw, peer)
        else:
            dest = self._session_manager.get_peer_address(session_id, dest_id)
            if dest is None:
                logger.debug(
                    "Unroutable from %s: destination client %d not found", source, dest_id
                )
                return
            self._socket.send(raw, dest)

    def _send_deny(self, dest: tuple[str, int], reason: str) -> None:
        self._socket.send_packet(
            NeonPacket.create(PacketType.CONNECT_DENY, 0, 0, 0, ConnectDeny(reason)),
            dest,
        )

    # ------------------------------------------------------------------ #
    # Cleanup                                                              #
    # ------------------------------------------------------------------ #

    def _perform_cleanup(self) -> None:
        now = time.monotonic()
        if now - self._last_cleanup < self._config.relay_cleanup_interval_ms / 1000.0:
            return
        self._last_cleanup = now

        self._session_manager.remove_stale(self._config.relay_client_timeout_ms)

        cutoff = now - self._config.relay_client_timeout_ms / 1000.0
        stale_conns = [
            addr
            for addr, conn in self._pending_connections.items()
            if conn.requested_at < cutoff
        ]
        for addr in stale_conns:
            conn = self._pending_connections.pop(addr)
            queue = self._pending_by_session.get(conn.session_id)
            if queue is not None:
                try:
                    queue.remove(addr)
                except ValueError:
                    pass
                if not queue:
                    self._pending_by_session.pop(conn.session_id, None)

        stale_rc = [
            k for k, rc in self._pending_reconnects.items() if rc.requested_at < cutoff
        ]
        for k in stale_rc:
            del self._pending_reconnects[k]

        limiter_expiry_s = self._config.relay_client_timeout_ms / 1000.0
        stale_limiters = [
            k for k, v in self._rate_limiters.items()
            if now - v.last_access > limiter_expiry_s
        ]
        for k in stale_limiters:
            del self._rate_limiters[k]


# Local imports to avoid forward-reference issues in match statements
from ._protocol import HostRegister as _HostRegister
from ._protocol import ConnectRequest as _ConnectRequest
from ._protocol import ReconnectRequest as _ReconnectRequest
