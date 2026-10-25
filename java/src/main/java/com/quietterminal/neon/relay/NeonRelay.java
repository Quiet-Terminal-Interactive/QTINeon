package com.quietterminal.neon.relay;

import com.quietterminal.neon.core.*;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.net.SocketAddress;
import java.time.Instant;
import java.util.ArrayDeque;
import java.util.Deque;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.logging.Logger;

/**
 * UDP relay server for the Neon multiplayer protocol.
 *
 * <p>
 * A {@code NeonRelay} routes packets between session hosts and clients. It
 * manages session
 * state, enforces per-source rate limits, and handles the connection and
 * reconnection handshake
 * on behalf of the host.
 *
 * <p>
 * The relay is stateless with respect to game logic — it only understands the
 * protocol
 * lifecycle packets ({@code CONNECT_REQUEST}, {@code CONNECT_ACCEPT}, etc.) and
 * routes everything
 * else based on the destination ID in the packet header.
 *
 * <p>
 * Typical deployment:
 * 
 * <pre>{@code
 * NeonRelay relay = new NeonRelay("0.0.0.0", NeonConfig.defaults());
 * Thread.ofVirtual().start(relay::startAndRun);
 * }</pre>
 *
 * <p>
 * Subclasses can intercept packets by overriding
 * {@link #onPacket(NeonPacket, SocketAddress)} and reply with
 * {@link #send(NeonPacket, SocketAddress)}:
 *
 * <pre>{@code
 * NeonRelay relay = new NeonRelay("0.0.0.0", NeonConfig.defaults()) {
 *     @Override
 *     protected boolean onPacket(NeonPacket packet, SocketAddress source) throws IOException {
 *         return false; // fall through to default handling
 *     }
 * };
 * }</pre>
 */
public class NeonRelay extends AbstractLifecycle implements AutoCloseable {

    private static final Logger logger = Logger.getLogger(NeonRelay.class.getName());

    private final NeonSocket socket;
    private final NeonConfig config;
    private final SessionManager sessionManager = new SessionManager();
    private final RelaySemantics relaySemantics = new RelaySemantics();
    private final ConcurrentHashMap<SocketAddress, RateLimiter> rateLimiters = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<SocketAddress, PendingConnection> pendingConnections = new ConcurrentHashMap<>();
    private final Map<Integer, Deque<SocketAddress>> pendingBySession = new HashMap<>();
    private final ConcurrentHashMap<String, PendingReconnect> pendingReconnects = new ConcurrentHashMap<>();
    private volatile long lastCleanupMs = System.currentTimeMillis();

    /**
     * Creates a relay bound to the given address on the port specified by
     * {@link NeonConfig#getRelayPort()}.
     *
     * @param bindAddress IP address to bind (e.g. {@code "0.0.0.0"} for all
     *                    interfaces)
     * @param config      protocol configuration
     * @throws IOException if the socket cannot be bound
     */
    public NeonRelay(String bindAddress, NeonConfig config) throws IOException {
        this.config = config;
        InetSocketAddress addr = new InetSocketAddress(bindAddress, config.getRelayPort());
        this.socket = new NeonSocket(addr, config);
    }

    @Override
    protected void doStart() {
        if (config.isDtlsEnabled())
            socket.enableServerDtls(config.getSslContext());
        logger.info("NeonRelay started on port " + config.getRelayPort());
    }

    @Override
    protected void doStop() throws IOException {
        socket.close();
    }

    /**
     * Starts the relay and runs the packet processing loop until {@link #stop()} is
     * called.
     * Intended to run in a dedicated virtual thread.
     *
     * @throws Exception if startup fails
     */
    public void startAndRun() throws Exception {
        start();
        run();
    }

    private void run() {
        while (isRunning()) {
            try {
                processPackets();
                performCleanup();
                Thread.sleep(config.getRelayMainLoopSleepMs());
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                break;
            } catch (IOException e) {
                if (!socket.isClosed()) {
                    logger.warning("IO error in relay loop: " + e.getMessage());
                }
                break;
            }
        }
    }

    private void processPackets() throws IOException {
        Transport.ReceivedData data;
        while ((data = socket.receive()) != null) {
            final Transport.ReceivedData received = data;
            sessionManager.updateLastSeen(received.source());
            NeonPacket packet;
            try {
                packet = NeonPacket.fromBytes(received.data());
            } catch (IllegalArgumentException e) {
                logger.fine(() -> "Malformed packet discarded from " + received.source());
                continue;
            }
            RateLimiter limiter = rateLimiters.get(received.source());

            if (limiter == null) {
                if (rateLimiters.size() >= config.getMaxRateLimiters()) {
                    logger.fine(() -> "Rate limiter capacity reached; packet discarded from " + received.source());
                    continue;
                }

                limiter = new RateLimiter(config.getMaxPacketsPerSecond());
                rateLimiters.put(received.source(), limiter);
            }

            if (!limiter.allowPacket()) {
                logger.fine(() -> "Rate limited: " + received.source());
                continue;
            }
            handlePacket(packet, received.source());
        }
    }

    /**
     * Called for every packet that passes rate limiting, before the relay's
     * default handling. The default implementation does nothing.
     *
     * @param packet the received packet
     * @param source the address the packet came from
     * @return {@code true} to consume the packet and skip default handling
     * @throws IOException if sending a reply fails
     */
    protected boolean onPacket(NeonPacket packet, SocketAddress source) throws IOException {
        return false;
    }

    /**
     * Sends a packet to the given address, encrypting via DTLS if a session
     * exists.
     *
     * @param packet the packet to send
     * @param dest   destination address
     * @throws IOException if the send fails
     */
    protected void send(NeonPacket packet, SocketAddress dest) throws IOException {
        socket.sendPacket(packet, dest);
    }

    private void handlePacket(NeonPacket packet, SocketAddress source) throws IOException {
        if (onPacket(packet, source))
            return;
        switch (packet.payload()) {
            case PacketPayload.HostRegister req -> handleHostRegister(req, source);
            case PacketPayload.ConnectRequest req -> handleConnectRequest(req, source, packet.header());
            case PacketPayload.ConnectAccept accept -> handleConnectAccept(accept, source, packet.header());
            case PacketPayload.ReconnectRequest req -> handleReconnectRequest(req, source);
            case @SuppressWarnings("unused") PacketPayload.DisconnectNotice ignored ->
                handleDisconnectNotice(source, packet.header());
            default -> routePacket(packet, source);
        }
    }

    private void handleHostRegister(PacketPayload.HostRegister req, SocketAddress source) throws IOException {
        if (req.sessionId() <= 0) {
            logger.warning("Rejected HOST_REGISTER: invalid sessionId=" + req.sessionId());
            return;
        }
        sessionManager.registerHost(req.sessionId(), source);
        logger.info("Host registered for session " + req.sessionId() + " from " + source);
        var accept = new PacketPayload.ConnectAccept((byte) 1, req.sessionId(), req.hostToken());
        socket.sendPacket(
                NeonPacket.create(PacketType.CONNECT_ACCEPT, (short) 0, (byte) 0, (byte) 1, accept),
                source);
    }

    private void handleConnectRequest(PacketPayload.ConnectRequest req, SocketAddress source, PacketHeader header)
            throws IOException {
        int sessionId = req.sessionId();
        SocketAddress host = sessionManager.getHost(sessionId);
        if (host == null) {
            sendDeny(source, "Session not found");
            return;
        }
        if (sessionManager.getClientCount(sessionId) >= config.getMaxClientsPerSession()) {
            sendDeny(source, "Session full");
            return;
        }
        if (pendingConnections.size() >= config.getMaxPendingConnections()) {
            sendDeny(source, "Relay busy");
            return;
        }
        pendingConnections.put(source, new PendingConnection(sessionId, req.name(), Instant.now()));
        pendingBySession.computeIfAbsent(sessionId, k -> new ArrayDeque<>()).addLast(source);
        socket.send(NeonPacket.create(PacketType.CONNECT_REQUEST,
                header.sequence(), header.clientId(), (byte) 1, req).toBytes(), host);
    }

    private void handleConnectAccept(PacketPayload.ConnectAccept accept, SocketAddress source, PacketHeader header)
            throws IOException {
        byte clientId = accept.clientId();
        int sessionId = accept.sessionId();

        if (clientId == 1) {
            logger.fine(() -> "Ignoring CONNECT_ACCEPT(clientId=1) from " + source);
            return;
        }

        String reconnectKey = sessionId + ":" + (clientId & 0xFF);
        PendingReconnect reconnect = pendingReconnects.remove(reconnectKey);
        if (reconnect != null) {
            sessionManager.updatePeerAddress(sessionId, clientId, reconnect.newAddress());
            socket.send(NeonPacket.create(PacketType.CONNECT_ACCEPT,
                    header.sequence(), header.clientId(), clientId, accept).toBytes(),
                    reconnect.newAddress());
            logger.info("Reconnect confirmed: client " + (clientId & 0xFF) + " session " + sessionId);
            return;
        }

        Deque<SocketAddress> queue = pendingBySession.get(sessionId);
        SocketAddress clientAddr = (queue != null) ? queue.pollFirst() : null;
        if (clientAddr == null) {
            logger.warning("CONNECT_ACCEPT for session " + sessionId + " but no pending clients");
            return;
        }
        if (queue.isEmpty())
            pendingBySession.remove(sessionId);
        pendingConnections.remove(clientAddr);
        sessionManager.registerPeer(sessionId, clientId, clientAddr);
        socket.send(NeonPacket.create(PacketType.CONNECT_ACCEPT,
                header.sequence(), header.clientId(), clientId, accept).toBytes(), clientAddr);
        logger.info("Client " + (clientId & 0xFF) + " joined session " + sessionId);
    }

    private void handleReconnectRequest(PacketPayload.ReconnectRequest req, SocketAddress source) throws IOException {
        int sessionId = req.sessionId();
        SocketAddress host = sessionManager.getHost(sessionId);
        if (host == null) {
            sendDeny(source, "Session not found");
            return;
        }
        String key = sessionId + ":" + (req.previousClientId() & 0xFF);
        boolean newReconnect = !pendingReconnects.containsKey(key);
        if (newReconnect && pendingReconnects.size() >= config.getMaxPendingConnections()) {
            sendDeny(source, "Relay busy");
            return;
        }
        pendingReconnects.put(key, new PendingReconnect(sessionId, req.previousClientId(), source, Instant.now()));
        if (newReconnect) {
            socket.send(NeonPacket.create(PacketType.RECONNECT_REQUEST,
                    (short) 0, req.previousClientId(), (byte) 1, req).toBytes(), host);
        }
    }

    private void handleDisconnectNotice(SocketAddress source, PacketHeader header) throws IOException {
        Integer sessionId = sessionManager.getSessionForPeer(source);
        if (sessionId != null) {
            byte[] notice = NeonPacket.create(PacketType.DISCONNECT_NOTICE,
                    header.sequence(), header.clientId(), (byte) 0,
                    new PacketPayload.DisconnectNotice()).toBytes();
            for (SocketAddress peer : sessionManager.getAllPeersExcept(sessionId, source)) {
                socket.send(notice, peer);
            }
        }
        sessionManager.removePeer(source);
        pendingConnections.remove(source);
        rateLimiters.remove(source);
        socket.removeDtlsSession(source);
    }

    private void routePacket(NeonPacket packet, SocketAddress source) throws IOException {
        RelaySemantics.RoutingDecision decision = relaySemantics.determineRouting(packet, source, sessionManager);
        byte[] bytes = packet.toBytes();
        switch (decision) {
            case RelaySemantics.RoutingDecision.Unicast u ->
                socket.send(bytes, u.destination());
            case RelaySemantics.RoutingDecision.Broadcast b -> {
                for (SocketAddress dest : b.destinations())
                    socket.send(bytes, dest);
            }
            case RelaySemantics.RoutingDecision.Unroutable u ->
                logger.fine(() -> "Unroutable from " + source + ": " + u.reason());
            case @SuppressWarnings("unused") RelaySemantics.RoutingDecision.RelayHandled ignored -> {
            }
        }
    }

    private void sendDeny(SocketAddress dest, String reason) throws IOException {
        socket.sendPacket(NeonPacket.create(PacketType.CONNECT_DENY, (short) 0, (byte) 0, (byte) 0,
                new PacketPayload.ConnectDeny(reason)), dest);
    }

    private void performCleanup() {
        long now = System.currentTimeMillis();
        if (now - lastCleanupMs < config.getRelayCleanupIntervalMs())
            return;
        lastCleanupMs = now;

        sessionManager.removeStale(config.getRelayClientTimeoutMs());

        Instant cutoff = Instant.now().minusMillis(config.getRelayClientTimeoutMs());
        pendingConnections.entrySet().removeIf(entry -> {
            if (entry.getValue().requestedAt().isBefore(cutoff)) {
                Deque<SocketAddress> queue = pendingBySession.get(entry.getValue().sessionId());
                if (queue != null) {
                    queue.remove(entry.getKey());
                    if (queue.isEmpty())
                        pendingBySession.remove(entry.getValue().sessionId());
                }
                return true;
            }
            return false;
        });

        pendingReconnects.entrySet().removeIf(e -> e.getValue().requestedAt().isBefore(cutoff));

        long limiterNowMs = System.currentTimeMillis();
        long limiterExpiryMs = config.getRelayClientTimeoutMs();

        rateLimiters.entrySet().removeIf(
                entry -> limiterNowMs - entry.getValue().lastAccessMs() > limiterExpiryMs);
    }

    /** Returns the local address the relay is bound to. */
    public InetSocketAddress getLocalAddress() throws IOException {
        return socket.getLocalAddress();
    }

    @Override
    public void close() throws Exception {
        if (isRunning())
            stop();
        else if (!socket.isClosed())
            socket.close();
    }
}
