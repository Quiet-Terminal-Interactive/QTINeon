package qtineon

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var hostLogger = slog.Default().With("component", "host")

type hostState uint8

const (
	hostCreated hostState = iota
	hostStarting
	hostRunning
	hostStopping
	hostStopped
	hostFailed
)

type disconnectedClient struct {
	name           string
	token          uint64
	disconnectedAt time.Time
}

// randomUint64 generates a cryptographically random 64-bit value, used for session and reconnect tokens.
func randomUint64() (uint64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

// NeonHost is a session host for the Neon multiplayer protocol.
//
// Registers a session with a NeonRelay, accepts client connections, manages session state, and reliably delivers SessionConfig to each connecting client.
// The host always occupies client ID 1; player IDs start at 2.
//
//	host, err := qtineon.NewHost(42, "relay.example.com:7777", qtineon.DefaultConfig())
//	host.SetClientConnectCallback(func(cid uint8, name string, sid int32) { ... })
//	go host.StartAndRun()
//	// … later …
//	host.Stop()
type NeonHost struct {
	config     *NeonConfig
	sessionID  int32
	relayAddr  net.UDPAddr
	socket     *neonSocket
	ackMachine *ackStateMachine

	nextClientID atomic.Uint32
	nextSequence atomic.Uint32

	stateMu sync.Mutex
	state   hostState

	clientsMu           sync.Mutex
	connectedClients    map[uint8]string
	connectedNames      map[string]uint8
	clientTokens        map[uint8]uint64
	disconnectedClients map[uint8]*disconnectedClient
	seqToClient         map[uint16]uint8

	mu                       sync.Mutex
	clientConnectCallback    func(clientID uint8, name string, sessionID int32)
	clientDenyCallback       func(name, reason string)
	clientDisconnectCallback func(clientID uint8)
	pingReceivedCallback     func(clientID uint8)
	unhandledPacketCallback  func(packetType, sender uint8, payload []byte)
	gamePacketRegistry       *GamePacketRegistry
}

// NewHost creates a host for sessionID that will register with relayAddress ("host:port").
func NewHost(sessionID int32, relayAddress string, config *NeonConfig) (*NeonHost, error) {
	if config == nil {
		config = DefaultConfig()
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	relayAddr, err := parseAddress(relayAddress)
	if err != nil {
		return nil, err
	}
	socket, err := newNeonSocket(&net.UDPAddr{}, config)
	if err != nil {
		return nil, err
	}

	h := &NeonHost{
		config:              config,
		sessionID:           sessionID,
		relayAddr:           relayAddr,
		socket:              socket,
		ackMachine:          newAckStateMachine(config.HostAckTimeoutMs, config.HostMaxAckRetries),
		connectedClients:    make(map[uint8]string),
		connectedNames:      make(map[string]uint8),
		clientTokens:        make(map[uint8]uint64),
		disconnectedClients: make(map[uint8]*disconnectedClient),
		seqToClient:         make(map[uint16]uint8),
		state:               hostCreated,
	}
	h.nextClientID.Store(2)
	return h, nil
}

// SetClientConnectCallback sets a callback fired when a client joins or rejoins, with (clientID, playerName, sessionID).
func (h *NeonHost) SetClientConnectCallback(callback func(clientID uint8, name string, sessionID int32)) {
	h.mu.Lock()
	h.clientConnectCallback = callback
	h.mu.Unlock()
}

// SetClientDenyCallback sets a callback fired when a connection request is denied, with (playerName, reason).
func (h *NeonHost) SetClientDenyCallback(callback func(name, reason string)) {
	h.mu.Lock()
	h.clientDenyCallback = callback
	h.mu.Unlock()
}

// SetClientDisconnectCallback sets a callback fired when a client disconnects, with the disconnecting clientID.
func (h *NeonHost) SetClientDisconnectCallback(callback func(clientID uint8)) {
	h.mu.Lock()
	h.clientDisconnectCallback = callback
	h.mu.Unlock()
}

// SetPingReceivedCallback sets a callback fired when a PING is received, with the sender's clientID.
func (h *NeonHost) SetPingReceivedCallback(callback func(clientID uint8)) {
	h.mu.Lock()
	h.pingReceivedCallback = callback
	h.mu.Unlock()
}

// SetUnhandledPacketCallback sets a callback fired for game-specific packets, with (packetType, senderClientID, payload).
func (h *NeonHost) SetUnhandledPacketCallback(callback func(packetType, sender uint8, payload []byte)) {
	h.mu.Lock()
	h.unhandledPacketCallback = callback
	h.mu.Unlock()
}

// SetGamePacketRegistry sets the registry of game packet types advertised to connecting clients.
//
// When not set, an empty PacketTypeRegistry is sent.
func (h *NeonHost) SetGamePacketRegistry(registry *GamePacketRegistry) {
	h.mu.Lock()
	h.gamePacketRegistry = registry
	h.mu.Unlock()
}

// Start registers with the relay without entering the processing loop.
//
// Blocks until the relay acknowledges the registration or the connection timeout elapses.
// Call StartAndRun for typical use.
func (h *NeonHost) Start() error {
	h.stateMu.Lock()
	if h.state != hostCreated {
		state := h.state
		h.stateMu.Unlock()
		return fmt.Errorf("cannot start from state: %v", state)
	}
	h.state = hostStarting
	h.stateMu.Unlock()

	if err := h.doStart(); err != nil {
		h.stateMu.Lock()
		h.state = hostFailed
		h.stateMu.Unlock()
		return err
	}
	h.stateMu.Lock()
	h.state = hostRunning
	h.stateMu.Unlock()
	return nil
}

func (h *NeonHost) doStart() error {
	if h.config.IsDTLSEnabled() {
		timeout := time.Duration(h.config.ClientConnectionTimeoutMs) * time.Millisecond
		if err := h.socket.performClientHandshake(h.config.DTLS, h.relayAddr, timeout); err != nil {
			return err
		}
	}

	hostToken, err := randomUint64()
	if err != nil {
		return err
	}
	h.clientTokens[1] = hostToken

	if err := h.socket.sendPacket(
		NewNeonPacket(HostRegisterType, h.nextSeq(), 1, 0, HostRegister{SessionID: h.sessionID, HostToken: hostToken}),
		h.relayAddr,
	); err != nil {
		return err
	}

	timeout := time.Duration(h.config.ClientConnectionTimeoutMs) * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("relay registration timed out")
		}
		packet, _, ok := h.socket.receivePacketTimeout(remaining)
		if !ok {
			return fmt.Errorf("no response from relay during registration")
		}
		if accept, ok := packet.Payload.(ConnectAccept); ok && accept.ClientID == 1 {
			hostLogger.Info("registered with relay", "sessionID", h.sessionID)
			return nil
		}
	}
}

// Stop sends a disconnect notice, drains pending ACKs, and closes the socket.
func (h *NeonHost) Stop() error {
	h.stateMu.Lock()
	if h.state != hostRunning {
		state := h.state
		h.stateMu.Unlock()
		return fmt.Errorf("cannot stop from state: %v", state)
	}
	h.state = hostStopping
	h.stateMu.Unlock()

	h.doStop()

	h.stateMu.Lock()
	h.state = hostStopped
	h.stateMu.Unlock()
	return nil
}

func (h *NeonHost) doStop() {
	_ = h.socket.sendPacket(
		NewNeonPacket(DisconnectNoticeType, h.nextSeq(), 1, 0, DisconnectNotice{}),
		h.relayAddr,
	)

	deadline := time.Now().Add(time.Duration(h.config.HostGracefulShutdownTimeoutMs) * time.Millisecond)
	for h.ackMachine.hasPending() && time.Now().Before(deadline) {
		result := h.ackMachine.process()
		for _, seq := range result.needsRetry {
			if pkt, ok := h.ackMachine.markResent(seq); ok {
				_ = h.socket.sendPacket(pkt, h.relayAddr)
			}
		}
		for _, seq := range result.failed {
			h.ackMachine.acknowledge(seq)
		}
		time.Sleep(10 * time.Millisecond)
	}

	_ = h.socket.close()
}

// StartAndRun registers with the relay and blocks in the processing loop until Stop is called.
//
//	go host.StartAndRun()
func (h *NeonHost) StartAndRun() error {
	if err := h.Start(); err != nil {
		return err
	}
	h.run()
	return nil
}

func (h *NeonHost) run() {
	sleep := time.Duration(h.config.HostProcessingLoopSleepMs) * time.Millisecond
	for h.IsRunning() {
		h.processPackets()
		h.checkPendingAcks()
		time.Sleep(sleep)
	}
}

// IsRunning reports whether the host processing loop is active.
func (h *NeonHost) IsRunning() bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return h.state == hostRunning
}

// LocalAddress returns the local address the host's socket is bound to.
func (h *NeonHost) LocalAddress() *net.UDPAddr {
	return h.socket.localAddr()
}

func (h *NeonHost) processPackets() {
	for {
		packet, _, ok := h.socket.receivePacket()
		if !ok {
			break
		}
		h.handlePacket(packet)
	}
}

func (h *NeonHost) handlePacket(packet NeonPacket) {
	header := packet.Header
	switch payload := packet.Payload.(type) {
	case ConnectRequest:
		h.handleConnectRequest(payload, header)
	case ReconnectRequest:
		h.handleReconnectRequest(payload)
	case Ping:
		h.handlePing(payload, header)
	case Ack:
		h.handleAck(payload)
	case DisconnectNotice:
		h.handleDisconnect(header)
	default:
		h.dispatchToGame(header, packet.Payload)
	}
}

func (h *NeonHost) handleConnectRequest(req ConnectRequest, header PacketHeader) {
	h.clientsMu.Lock()
	if len(h.connectedClients) >= h.config.MaxClientsPerSession {
		h.clientsMu.Unlock()
		h.sendDeny(req.Name, "Session full")
		return
	}
	if _, taken := h.connectedNames[req.Name]; taken {
		h.clientsMu.Unlock()
		h.sendDeny(req.Name, "Name taken")
		return
	}

	cid32 := h.nextClientID.Add(1) - 1
	if cid32 == 0 || cid32 == 1 || cid32 > 254 {
		h.clientsMu.Unlock()
		h.sendDeny(req.Name, "Server full")
		return
	}
	cid := uint8(cid32)

	token, err := randomUint64()
	if err != nil {
		h.clientsMu.Unlock()
		h.sendDeny(req.Name, "Internal error")
		return
	}
	h.connectedClients[cid] = req.Name
	h.connectedNames[req.Name] = cid
	h.clientTokens[cid] = token
	h.clientsMu.Unlock()

	_ = h.socket.sendPacket(
		NewNeonPacket(ConnectAcceptType, h.nextSeq(), 1, 0, ConnectAccept{ClientID: cid, SessionID: h.sessionID, Token: token}),
		h.relayAddr,
	)

	h.mu.Lock()
	connectCallback := h.clientConnectCallback
	h.mu.Unlock()
	if connectCallback != nil {
		connectCallback(cid, req.Name, h.sessionID)
	}

	cfgSeq := h.nextSeq()
	cfgPacket := NewNeonPacket(
		SessionConfigType, cfgSeq, 1, cid,
		SessionConfig{Version: VERSION, TickRate: h.config.HostSessionTickRate, MaxPacketSize: h.config.HostSessionMaxPacketSize},
	)
	_ = h.socket.sendPacket(cfgPacket, h.relayAddr)
	h.ackMachine.track(cfgSeq, cfgPacket)
	h.clientsMu.Lock()
	h.seqToClient[cfgSeq] = cid
	h.clientsMu.Unlock()

	h.mu.Lock()
	registryHolder := h.gamePacketRegistry
	h.mu.Unlock()
	registry := PacketTypeRegistry{}
	if registryHolder != nil && !registryHolder.IsEmpty() {
		registry = registryHolder.BuildRegistry()
	}
	_ = h.socket.sendPacket(
		NewNeonPacket(PacketTypeRegistryType, h.nextSeq(), 1, cid, registry),
		h.relayAddr,
	)
	hostLogger.Info("client connected", "clientID", cid, "name", req.Name, "sessionID", h.sessionID)
}

func (h *NeonHost) handleReconnectRequest(req ReconnectRequest) {
	prevID := req.PreviousClientID

	h.clientsMu.Lock()
	dc, ok := h.disconnectedClients[prevID]
	if !ok {
		h.clientsMu.Unlock()
		h.sendDeny("", "Reconnect denied")
		return
	}
	if dc.token != req.Token {
		h.clientsMu.Unlock()
		h.sendDeny(dc.name, "Reconnect denied")
		return
	}
	if time.Since(dc.disconnectedAt) > time.Duration(h.config.HostSessionTokenTimeoutMs)*time.Millisecond {
		delete(h.disconnectedClients, prevID)
		h.clientsMu.Unlock()
		h.sendDeny(dc.name, "Token expired")
		return
	}

	delete(h.disconnectedClients, prevID)
	newToken, err := randomUint64()
	if err != nil {
		h.clientsMu.Unlock()
		h.sendDeny(dc.name, "Internal error")
		return
	}
	h.connectedClients[prevID] = dc.name
	h.connectedNames[dc.name] = prevID
	h.clientTokens[prevID] = newToken
	name := dc.name
	h.clientsMu.Unlock()

	_ = h.socket.sendPacket(
		NewNeonPacket(ConnectAcceptType, h.nextSeq(), 1, 0, ConnectAccept{ClientID: prevID, SessionID: h.sessionID, Token: newToken}),
		h.relayAddr,
	)
	h.mu.Lock()
	connectCallback := h.clientConnectCallback
	h.mu.Unlock()
	if connectCallback != nil {
		connectCallback(prevID, name, h.sessionID)
	}
	hostLogger.Info("client reconnected", "clientID", prevID, "name", name, "sessionID", h.sessionID)
}

func (h *NeonHost) handlePing(ping Ping, header PacketHeader) {
	_ = h.socket.sendPacket(
		NewNeonPacket(PongType, h.nextSeq(), 1, header.ClientID, Pong{OriginalTimestamp: ping.Timestamp}),
		h.relayAddr,
	)
	h.mu.Lock()
	pingCallback := h.pingReceivedCallback
	h.mu.Unlock()
	if pingCallback != nil {
		pingCallback(header.ClientID)
	}
}

func (h *NeonHost) handleAck(ack Ack) {
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()
	for _, seq := range ack.Sequences {
		h.ackMachine.acknowledge(seq)
		delete(h.seqToClient, seq)
	}
}

func (h *NeonHost) handleDisconnect(header PacketHeader) {
	cid := header.ClientID
	h.clientsMu.Lock()
	name, ok := h.connectedClients[cid]
	if !ok {
		h.clientsMu.Unlock()
		return
	}
	delete(h.connectedClients, cid)
	delete(h.connectedNames, name)
	token := h.clientTokens[cid]
	h.disconnectedClients[cid] = &disconnectedClient{name: name, token: token, disconnectedAt: time.Now()}
	for seq, c := range h.seqToClient {
		if c == cid {
			delete(h.seqToClient, seq)
		}
	}
	h.clientsMu.Unlock()

	h.mu.Lock()
	disconnectCallback := h.clientDisconnectCallback
	h.mu.Unlock()
	if disconnectCallback != nil {
		disconnectCallback(cid)
	}
	hostLogger.Info("client disconnected", "clientID", cid, "name", name, "sessionID", h.sessionID)
}

func (h *NeonHost) dispatchToGame(header PacketHeader, payload Payload) {
	h.mu.Lock()
	callback := h.unhandledPacketCallback
	h.mu.Unlock()
	if callback == nil {
		return
	}
	var raw []byte
	if gp, ok := payload.(GamePacket); ok {
		raw = gp.RawPayload
	}
	callback(header.PacketType, header.ClientID, raw)
}

func (h *NeonHost) checkPendingAcks() {
	result := h.ackMachine.process()
	for _, seq := range result.needsRetry {
		if pkt, ok := h.ackMachine.markResent(seq); ok {
			_ = h.socket.sendPacket(pkt, h.relayAddr)
		}
	}
	for _, seq := range result.failed {
		hostLogger.Warn("ACK permanently failed", "sequence", seq)
		h.ackMachine.acknowledge(seq)
		h.clientsMu.Lock()
		delete(h.seqToClient, seq)
		h.clientsMu.Unlock()
	}
}

func (h *NeonHost) sendDeny(name, reason string) {
	if name != "" {
		h.mu.Lock()
		denyCallback := h.clientDenyCallback
		h.mu.Unlock()
		if denyCallback != nil {
			denyCallback(name, reason)
		}
	}
	_ = h.socket.sendPacket(
		NewNeonPacket(ConnectDenyType, h.nextSeq(), 1, 0, ConnectDeny{Reason: reason}),
		h.relayAddr,
	)
}

// SendPacket sends a game-specific payload through the relay.
//
// packetType must be >= 0x10. destID is the destination client ID, or 0 to broadcast to all peers.
func (h *NeonHost) SendPacket(payload []byte, packetType uint8, destID uint8) error {
	header := NewPacketHeader(packetType, h.nextSeq(), 1, destID)
	return h.socket.sendPacket(NeonPacket{Header: header, Payload: GamePacket{RawPayload: payload}}, h.relayAddr)
}

// ConnectedClients returns a snapshot of {clientID: playerName} for all connected clients.
func (h *NeonHost) ConnectedClients() map[uint8]string {
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()
	out := make(map[uint8]string, len(h.connectedClients))
	for k, v := range h.connectedClients {
		out[k] = v
	}
	return out
}

func (h *NeonHost) nextSeq() uint16 {
	v := h.nextSequence.Add(1) - 1
	return uint16(v & 0xFFFF)
}
