package qtineon

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var clientLogger = slog.Default().With("component", "client")

type clientState uint8

const (
	clientCreated clientState = iota
	clientStarting
	clientRunning
	clientStopping
	clientStopped
	clientFailed
)

// NeonClient is a relay-connected UDP client for the Neon multiplayer protocol.
//
// Connects to a session hosted by a NeonHost through a NeonRelay.
// The connection handshake is synchronous; ongoing packet processing is driven either by calling Run in a dedicated goroutine or by periodically calling ProcessPackets from the game loop.
//
//	client, err := qtineon.NewClient("player1", qtineon.DefaultConfig())
//	client.SetSessionConfigCallback(func(sc qtineon.SessionConfig) { ... })
//	if client.Connect(42, "relay.example.com:7777") {
//		go client.Run()
//	}
type NeonClient struct {
	name   string
	config *NeonConfig
	socket *neonSocket

	relayAddr    net.UDPAddr
	hasRelayAddr bool
	sessionID    int32
	clientID     uint8
	hasClientID  bool
	sessionToken uint64

	nextSequence atomic.Uint32

	stateMu sync.Mutex
	state   clientState

	lastPingMu sync.Mutex
	lastPing   time.Time

	mu                         sync.Mutex
	pongCallback               func(Pong)
	sessionConfigCallback      func(SessionConfig)
	packetTypeRegistryCallback func(PacketTypeRegistry)
	unhandledPacketCallback    func(packetType, sender uint8, payload []byte)
	disconnectCallback         func(clientID uint8)
}

// NewClient creates a client with the given display name.
//
// Binds a local UDP socket on an OS-assigned port.
func NewClient(name string, config *NeonConfig) (*NeonClient, error) {
	if config == nil {
		config = DefaultConfig()
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	socket, err := newNeonSocket(&net.UDPAddr{}, config)
	if err != nil {
		return nil, err
	}
	return &NeonClient{
		name:     name,
		config:   config,
		socket:   socket,
		state:    clientCreated,
		lastPing: time.Now(),
	}, nil
}

// SetPongCallback sets a callback fired when a Pong is received.
func (c *NeonClient) SetPongCallback(callback func(Pong)) {
	c.mu.Lock()
	c.pongCallback = callback
	c.mu.Unlock()
}

// SetSessionConfigCallback sets a callback fired when the host delivers a SessionConfig.
//
// The client ACKs automatically; the callback is for application use only.
func (c *NeonClient) SetSessionConfigCallback(callback func(SessionConfig)) {
	c.mu.Lock()
	c.sessionConfigCallback = callback
	c.mu.Unlock()
}

// SetPacketTypeRegistryCallback sets a callback fired when the host broadcasts its PacketTypeRegistry.
func (c *NeonClient) SetPacketTypeRegistryCallback(callback func(PacketTypeRegistry)) {
	c.mu.Lock()
	c.packetTypeRegistryCallback = callback
	c.mu.Unlock()
}

// SetUnhandledPacketCallback sets a callback fired for packets not handled by the protocol layer, with (packetType, senderClientID, payload).
func (c *NeonClient) SetUnhandledPacketCallback(callback func(packetType, sender uint8, payload []byte)) {
	c.mu.Lock()
	c.unhandledPacketCallback = callback
	c.mu.Unlock()
}

// SetDisconnectCallback sets a callback fired when a DisconnectNotice is received, with the disconnecting peer's clientID.
func (c *NeonClient) SetDisconnectCallback(callback func(clientID uint8)) {
	c.mu.Lock()
	c.disconnectCallback = callback
	c.mu.Unlock()
}

// Connect connects to sessionID through relayAddress ("host:port").
//
// Blocks until the relay accepts or denies the connection, or until the connection timeout elapses.
// Returns false if denied or timed out.
func (c *NeonClient) Connect(sessionID int32, relayAddress string) bool {
	relayAddr, err := parseAddress(relayAddress)
	if err != nil {
		clientLogger.Warn("connection failed", "error", err)
		return false
	}
	c.sessionID = sessionID
	c.relayAddr = relayAddr
	c.hasRelayAddr = true

	if err := c.start(); err != nil {
		clientLogger.Warn("connection failed", "error", err)
		return false
	}
	return true
}

func (c *NeonClient) start() error {
	c.stateMu.Lock()
	if c.state != clientCreated {
		state := c.state
		c.stateMu.Unlock()
		return fmt.Errorf("cannot start from state: %v", state)
	}
	c.state = clientStarting
	c.stateMu.Unlock()

	if err := c.doStart(); err != nil {
		c.stateMu.Lock()
		c.state = clientFailed
		c.stateMu.Unlock()
		return err
	}
	c.stateMu.Lock()
	c.state = clientRunning
	c.stateMu.Unlock()
	return nil
}

func (c *NeonClient) doStart() error {
	timeout := time.Duration(c.config.ClientConnectionTimeoutMs) * time.Millisecond

	if c.config.IsDTLSEnabled() {
		if err := c.socket.performClientHandshake(c.config.DTLS, c.relayAddr, timeout); err != nil {
			return err
		}
	}

	if err := c.socket.sendPacket(
		NewNeonPacket(ConnectRequestType, c.nextSeq(), 0, 1, ConnectRequest{
			ClientVersion: VERSION,
			Name:          c.name,
			SessionID:     c.sessionID,
			GameID:        0,
		}),
		c.relayAddr,
	); err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("connection timed out")
		}
		packet, _, ok := c.socket.receivePacketTimeout(remaining)
		if !ok {
			return fmt.Errorf("no response from relay")
		}
		switch payload := packet.Payload.(type) {
		case ConnectAccept:
			c.clientID = payload.ClientID
			c.hasClientID = true
			c.sessionToken = payload.Token
			clientLogger.Info("connected", "clientID", c.clientID, "sessionID", c.sessionID)
			return nil
		case ConnectDeny:
			return fmt.Errorf("connection denied: %s", payload.Reason)
		}
	}
}

// Stop sends a disconnect notice and closes the socket.
func (c *NeonClient) Stop() error {
	c.stateMu.Lock()
	if c.state != clientRunning {
		state := c.state
		c.stateMu.Unlock()
		return fmt.Errorf("cannot stop from state: %v", state)
	}
	c.state = clientStopping
	c.stateMu.Unlock()

	c.doStop()

	c.stateMu.Lock()
	c.state = clientStopped
	c.stateMu.Unlock()
	return nil
}

func (c *NeonClient) doStop() {
	cid := c.clientID
	_ = c.socket.sendPacket(
		NewNeonPacket(DisconnectNoticeType, c.nextSeq(), cid, 0, DisconnectNotice{}),
		c.relayAddr,
	)
	_ = c.socket.close()
}

// IsRunning reports whether the client processing loop is active.
func (c *NeonClient) IsRunning() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state == clientRunning
}

// ClientID returns the client ID assigned by the host, and whether it has been assigned yet.
func (c *NeonClient) ClientID() (uint8, bool) {
	return c.clientID, c.hasClientID
}

// LocalAddress returns the local address the client's socket is bound to.
func (c *NeonClient) LocalAddress() *net.UDPAddr {
	return c.socket.localAddr()
}

// Run drives the client processing loop until Stop is called.
//
// Drains inbound packets and fires auto-pings on each iteration.
// Intended to run in a dedicated goroutine:
//
//	go client.Run()
func (c *NeonClient) Run() {
	sleep := time.Duration(c.config.ClientProcessingLoopSleepMs) * time.Millisecond
	for c.IsRunning() {
		c.ProcessPackets()
		c.checkAutoPing()
		time.Sleep(sleep)
	}
}

// ProcessPackets drains all currently buffered inbound packets and fires callbacks.
//
// Use this instead of Run when integrating with an external game loop.
func (c *NeonClient) ProcessPackets() {
	for {
		packet, _, ok := c.socket.receivePacket()
		if !ok {
			break
		}
		c.handlePacket(packet)
	}
}

func (c *NeonClient) handlePacket(packet NeonPacket) {
	header := packet.Header
	if c.hasClientID && header.DestinationID != 0 && header.DestinationID != c.clientID {
		return
	}

	switch payload := packet.Payload.(type) {
	case Pong:
		c.mu.Lock()
		callback := c.pongCallback
		c.mu.Unlock()
		if callback != nil {
			callback(payload)
		}
	case SessionConfig:
		c.handleSessionConfig(payload, header.Sequence)
	case PacketTypeRegistry:
		c.mu.Lock()
		callback := c.packetTypeRegistryCallback
		c.mu.Unlock()
		if callback != nil {
			callback(payload)
		}
	case Ping:
		c.sendPong(payload.Timestamp)
	case DisconnectNotice:
		c.mu.Lock()
		callback := c.disconnectCallback
		c.mu.Unlock()
		if callback != nil {
			callback(header.ClientID)
		}
	default:
		c.mu.Lock()
		callback := c.unhandledPacketCallback
		c.mu.Unlock()
		if callback != nil {
			var raw []byte
			if gp, ok := packet.Payload.(GamePacket); ok {
				raw = gp.RawPayload
			}
			callback(header.PacketType, header.ClientID, raw)
		}
	}
}

func (c *NeonClient) handleSessionConfig(sc SessionConfig, sequence uint16) {
	c.mu.Lock()
	callback := c.sessionConfigCallback
	c.mu.Unlock()
	if callback != nil {
		callback(sc)
	}
	cid := c.clientID
	_ = c.socket.sendPacket(
		NewNeonPacket(AckType, c.nextSeq(), cid, 0, Ack{Sequences: []uint16{sequence}}),
		c.relayAddr,
	)
}

func (c *NeonClient) sendPong(timestamp uint64) {
	cid := c.clientID
	_ = c.socket.sendPacket(
		NewNeonPacket(PongType, c.nextSeq(), cid, 1, Pong{OriginalTimestamp: timestamp}),
		c.relayAddr,
	)
}

func (c *NeonClient) checkAutoPing() {
	c.lastPingMu.Lock()
	elapsed := time.Since(c.lastPing)
	due := elapsed.Milliseconds() >= int64(c.config.ClientPingIntervalMs)
	if due {
		c.lastPing = time.Now()
	}
	c.lastPingMu.Unlock()
	if !due {
		return
	}
	cid := c.clientID
	ts := uint64(time.Now().UnixMilli())
	_ = c.socket.sendPacket(
		NewNeonPacket(PingType, c.nextSeq(), cid, 1, Ping{Timestamp: ts}),
		c.relayAddr,
	)
}

// SendPacket sends a game-specific payload through the relay.
//
// packetType must be >= 0x10. destID is the destination client ID, or 0 to broadcast to all session peers.
func (c *NeonClient) SendPacket(payload []byte, packetType uint8, destID uint8) error {
	cid := c.clientID
	header := NewPacketHeader(packetType, c.nextSeq(), cid, destID)
	return c.socket.sendPacket(NeonPacket{Header: header, Payload: GamePacket{RawPayload: payload}}, c.relayAddr)
}

// Reconnect attempts to rejoin the last session using the stored session token.
//
// Uses exponential backoff between attempts.
// Creates a new socket if the existing one is closed. maxAttempts overrides config.ClientMaxReconnectAttempts when non-zero.
func (c *NeonClient) Reconnect(maxAttempts int) bool {
	if !c.hasRelayAddr || !c.hasClientID {
		clientLogger.Warn("cannot reconnect: missing session state")
		return false
	}

	attempts := c.config.ClientMaxReconnectAttempts
	if maxAttempts != 0 {
		attempts = maxAttempts
	}

	if c.socket.isClosed() {
		socket, err := newNeonSocket(&net.UDPAddr{}, c.config)
		if err != nil {
			clientLogger.Warn("failed to recreate socket for reconnect", "error", err)
			return false
		}
		c.socket = socket
		if c.config.IsDTLSEnabled() {
			timeout := time.Duration(c.config.ClientConnectionTimeoutMs) * time.Millisecond
			if err := c.socket.performClientHandshake(c.config.DTLS, c.relayAddr, timeout); err != nil {
				clientLogger.Warn("failed to recreate socket for reconnect", "error", err)
				return false
			}
		}
	}

	delay := time.Duration(c.config.ClientInitialReconnectDelayMs) * time.Millisecond
	maxDelay := time.Duration(c.config.ClientMaxReconnectDelayMs) * time.Millisecond
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
		if c.attemptReconnect() {
			return true
		}
	}
	return false
}

func (c *NeonClient) attemptReconnect() bool {
	if err := c.socket.sendPacket(
		NewNeonPacket(ReconnectRequestType, c.nextSeq(), c.clientID, 1, ReconnectRequest{
			Token:            c.sessionToken,
			SessionID:        c.sessionID,
			PreviousClientID: c.clientID,
		}),
		c.relayAddr,
	); err != nil {
		clientLogger.Warn("reconnect attempt failed", "error", err)
		return false
	}

	timeout := time.Duration(c.config.ClientConnectionTimeoutMs) * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		packet, _, ok := c.socket.receivePacketTimeout(remaining)
		if !ok {
			return false
		}
		switch payload := packet.Payload.(type) {
		case ConnectAccept:
			c.sessionToken = payload.Token
			clientLogger.Info("reconnected", "clientID", c.clientID)
			return true
		case ConnectDeny:
			clientLogger.Warn("reconnect denied", "reason", payload.Reason)
			return false
		}
	}
}

func (c *NeonClient) nextSeq() uint16 {
	v := c.nextSequence.Add(1) - 1
	return uint16(v & 0xFFFF)
}
