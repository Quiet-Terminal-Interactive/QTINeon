package qtineon

import (
	"fmt"
	"log/slog"
	"net"
	"time"
)

var relayLogger = slog.Default().With("component", "relay")

type relayState uint8

const (
	relayCreated relayState = iota
	relayRunning
	relayStopped
	relayFailed
)

type pendingConnection struct {
	sessionID   int32
	name        string
	addr        net.UDPAddr
	requestedAt time.Time
}

type pendingReconnect struct {
	sessionID   int32
	clientID    uint8
	newAddress  net.UDPAddr
	requestedAt time.Time
}

// NeonRelay is a UDP relay server for the Neon multiplayer protocol.
//
// Routes packets between session hosts and clients.
// The relay is stateless with respect to game logic, it only understands protocol lifecycle packets and routes everything else by destination ID.
//
//	relay, err := qtineon.NewRelay("0.0.0.0", qtineon.DefaultConfig())
//	go relay.StartAndRun()
//	// … later …
//	relay.Stop()
type NeonRelay struct {
	config *NeonConfig
	socket *neonSocket

	sessions     *sessionManager
	rateLimiters map[string]*rateLimiter

	// pendingConnections and pendingBySession are only touched from the relay processing goroutine, no locking required.
	pendingConnections map[string]*pendingConnection
	pendingBySession   map[int32][]net.UDPAddr
	pendingReconnects  map[string]*pendingReconnect
	lastCleanup        time.Time

	state relayState
}

// NewRelay creates a relay bound to bindAddress on config.RelayPort.
func NewRelay(bindAddress string, config *NeonConfig) (*NeonRelay, error) {
	if config == nil {
		config = DefaultConfig()
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	laddr := &net.UDPAddr{IP: net.ParseIP(bindAddress), Port: config.RelayPort}
	socket, err := newNeonSocket(laddr, config)
	if err != nil {
		return nil, err
	}
	return &NeonRelay{
		config:             config,
		socket:             socket,
		sessions:           newSessionManager(),
		rateLimiters:       make(map[string]*rateLimiter),
		pendingConnections: make(map[string]*pendingConnection),
		pendingBySession:   make(map[int32][]net.UDPAddr),
		pendingReconnects:  make(map[string]*pendingReconnect),
		lastCleanup:        time.Now(),
		state:              relayCreated,
	}, nil
}

// Start initialises the relay without entering the processing loop.
//
// Call StartAndRun instead for typical use.
func (r *NeonRelay) Start() error {
	if r.state != relayCreated {
		return fmt.Errorf("cannot start from state: %v", r.state)
	}
	r.state = relayRunning
	if r.config.IsDTLSEnabled() {
		r.socket.enableServerDTLS(r.config.DTLS)
	}
	relayLogger.Info("NeonRelay started", "port", r.config.RelayPort)
	return nil
}

// Stop signals the relay to stop and closes the socket.
func (r *NeonRelay) Stop() error {
	if r.state != relayRunning {
		return fmt.Errorf("cannot stop from state: %v", r.state)
	}
	r.state = relayStopped
	return r.socket.close()
}

// StartAndRun starts the relay and blocks in the processing loop until Stop is called.
//
//	go relay.StartAndRun()
func (r *NeonRelay) StartAndRun() error {
	if err := r.Start(); err != nil {
		return err
	}
	r.run()
	return nil
}

func (r *NeonRelay) run() {
	sleep := time.Duration(r.config.RelayMainLoopSleepMs) * time.Millisecond
	for r.state == relayRunning {
		r.processPackets()
		r.performCleanup()
		time.Sleep(sleep)
	}
}

// IsRunning reports whether the relay processing loop is active.
func (r *NeonRelay) IsRunning() bool {
	return r.state == relayRunning
}

// LocalAddress returns the local address the relay is bound to.
func (r *NeonRelay) LocalAddress() *net.UDPAddr {
	return r.socket.localAddr()
}

func (r *NeonRelay) processPackets() {
	for {
		packet, source, ok := r.socket.receivePacket()
		if !ok {
			break
		}
		r.sessions.updateLastSeen(source)

		key := source.String()
		limiter, exists := r.rateLimiters[key]
		if !exists {
			limiter = newRateLimiter(r.config.MaxPacketsPerSecond)
			r.rateLimiters[key] = limiter
		}
		if !limiter.allowPacket() {
			relayLogger.Debug("rate limited", "source", source)
			continue
		}

		r.handlePacket(packet, source)
	}
}

func (r *NeonRelay) handlePacket(packet NeonPacket, source net.UDPAddr) {
	switch payload := packet.Payload.(type) {
	case HostRegister:
		r.handleHostRegister(payload, source)
	case ConnectRequest:
		r.handleConnectRequest(payload, source, packet.Header)
	case ConnectAccept:
		r.handleConnectAccept(payload, source, packet.Header)
	case ReconnectRequest:
		r.handleReconnectRequest(payload, source)
	case DisconnectNotice:
		r.handleDisconnectNotice(source, packet.Header)
	default:
		r.routePacket(packet, source)
	}
}

func (r *NeonRelay) handleHostRegister(req HostRegister, source net.UDPAddr) {
	if req.SessionID <= 0 {
		relayLogger.Warn("rejected HOST_REGISTER", "sessionID", req.SessionID)
		return
	}
	r.sessions.registerHost(req.SessionID, source)
	relayLogger.Info("host registered", "sessionID", req.SessionID, "source", source)
	accept := ConnectAccept{ClientID: 1, SessionID: req.SessionID, Token: req.HostToken}
	_ = r.socket.sendPacket(NewNeonPacket(ConnectAcceptType, 0, 0, 1, accept), source)
}

func (r *NeonRelay) handleConnectRequest(req ConnectRequest, source net.UDPAddr, header PacketHeader) {
	host, ok := r.sessions.getHost(req.SessionID)
	if !ok {
		r.sendDeny(source, "Session not found")
		return
	}
	if r.sessions.getClientCount(req.SessionID) >= r.config.MaxClientsPerSession {
		r.sendDeny(source, "Session full")
		return
	}
	if len(r.pendingConnections) >= r.config.MaxPendingConnections {
		r.sendDeny(source, "Relay busy")
		return
	}

	r.pendingConnections[source.String()] = &pendingConnection{
		sessionID:   req.SessionID,
		name:        req.Name,
		addr:        source,
		requestedAt: time.Now(),
	}
	r.pendingBySession[req.SessionID] = append(r.pendingBySession[req.SessionID], source)

	_ = r.socket.sendPacket(
		NewNeonPacket(ConnectRequestType, header.Sequence, header.ClientID, 1, req),
		host,
	)
}

func (r *NeonRelay) handleConnectAccept(accept ConnectAccept, source net.UDPAddr, header PacketHeader) {
	clientID := accept.ClientID
	sessionID := accept.SessionID

	if clientID == 1 {
		relayLogger.Debug("ignoring CONNECT_ACCEPT(client_id=1)", "source", source)
		return
	}

	reconnectKey := fmt.Sprintf("%d:%d", sessionID, clientID)
	if reconnect, ok := r.pendingReconnects[reconnectKey]; ok {
		delete(r.pendingReconnects, reconnectKey)
		r.sessions.updatePeerAddress(sessionID, clientID, reconnect.newAddress)
		_ = r.socket.sendPacket(
			NewNeonPacket(ConnectAcceptType, header.Sequence, header.ClientID, clientID, accept),
			reconnect.newAddress,
		)
		relayLogger.Info("reconnect confirmed", "clientID", clientID, "sessionID", sessionID)
		return
	}

	queue := r.pendingBySession[sessionID]
	if len(queue) == 0 {
		relayLogger.Warn("CONNECT_ACCEPT with no pending clients", "sessionID", sessionID)
		return
	}
	clientAddr := queue[0]
	queue = queue[1:]
	if len(queue) == 0 {
		delete(r.pendingBySession, sessionID)
	} else {
		r.pendingBySession[sessionID] = queue
	}

	delete(r.pendingConnections, clientAddr.String())
	r.sessions.registerPeer(sessionID, clientID, clientAddr)
	_ = r.socket.sendPacket(
		NewNeonPacket(ConnectAcceptType, header.Sequence, header.ClientID, clientID, accept),
		clientAddr,
	)
	relayLogger.Info("client joined session", "clientID", clientID, "sessionID", sessionID)
}

func (r *NeonRelay) handleReconnectRequest(req ReconnectRequest, source net.UDPAddr) {
	host, ok := r.sessions.getHost(req.SessionID)
	if !ok {
		r.sendDeny(source, "Session not found")
		return
	}
	key := fmt.Sprintf("%d:%d", req.SessionID, req.PreviousClientID)
	r.pendingReconnects[key] = &pendingReconnect{
		sessionID:   req.SessionID,
		clientID:    req.PreviousClientID,
		newAddress:  source,
		requestedAt: time.Now(),
	}
	_ = r.socket.sendPacket(
		NewNeonPacket(ReconnectRequestType, 0, req.PreviousClientID, 1, req),
		host,
	)
}

func (r *NeonRelay) handleDisconnectNotice(source net.UDPAddr, header PacketHeader) {
	if sessionID, ok := r.sessions.getSessionForPeer(source); ok {
		noticeBytes, err := NewNeonPacket(DisconnectNoticeType, header.Sequence, header.ClientID, 0, DisconnectNotice{}).ToBytes()
		if err == nil {
			for _, peer := range r.sessions.getAllPeersExcept(sessionID, source) {
				_ = r.socket.send(noticeBytes, peer)
			}
		}
	}

	r.sessions.removePeer(source)
	delete(r.pendingConnections, source.String())
	delete(r.rateLimiters, source.String())
	r.socket.removeDTLSSession(source)
}

func (r *NeonRelay) routePacket(packet NeonPacket, source net.UDPAddr) {
	sessionID, ok := r.sessions.getSessionForPeer(source)
	if !ok {
		relayLogger.Debug("unroutable: not in any session", "source", source)
		return
	}

	destID := packet.Header.DestinationID
	raw, err := packet.ToBytes()
	if err != nil {
		return
	}

	if destID == 0 {
		peers := r.sessions.getAllPeersExcept(sessionID, source)
		if len(peers) == 0 {
			relayLogger.Debug("unroutable: no peers to broadcast to", "source", source)
			return
		}
		for _, peer := range peers {
			_ = r.socket.send(raw, peer)
		}
	} else {
		dest, ok := r.sessions.getPeerAddress(sessionID, destID)
		if !ok {
			relayLogger.Debug("unroutable: destination not found", "source", source, "destID", destID)
			return
		}
		_ = r.socket.send(raw, dest)
	}
}

func (r *NeonRelay) sendDeny(dest net.UDPAddr, reason string) {
	_ = r.socket.sendPacket(NewNeonPacket(ConnectDenyType, 0, 0, 0, ConnectDeny{reason}), dest)
}

func (r *NeonRelay) performCleanup() {
	now := time.Now()
	interval := time.Duration(r.config.RelayCleanupIntervalMs) * time.Millisecond
	if now.Sub(r.lastCleanup) < interval {
		return
	}
	r.lastCleanup = now

	r.sessions.removeStale(r.config.RelayClientTimeoutMs)

	cutoff := now.Add(-time.Duration(r.config.RelayClientTimeoutMs) * time.Millisecond)

	for addrKey, conn := range r.pendingConnections {
		if conn.requestedAt.Before(cutoff) {
			delete(r.pendingConnections, addrKey)
			queue := r.pendingBySession[conn.sessionID]
			for i, a := range queue {
				if a.String() == addrKey {
					queue = append(queue[:i], queue[i+1:]...)
					break
				}
			}
			if len(queue) == 0 {
				delete(r.pendingBySession, conn.sessionID)
			} else {
				r.pendingBySession[conn.sessionID] = queue
			}
		}
	}

	for key, rc := range r.pendingReconnects {
		if rc.requestedAt.Before(cutoff) {
			delete(r.pendingReconnects, key)
		}
	}

	if len(r.rateLimiters) > r.config.MaxRateLimiters {
		r.rateLimiters = make(map[string]*rateLimiter)
	}
}
