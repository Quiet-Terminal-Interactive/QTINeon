package qtineon

import "fmt"

// NeonConfig holds configuration shared across NeonRelay, NeonHost, and NeonClient.
//
// Obtain defaults with DefaultConfig() and customise fields directly:
//
//	cfg := qtineon.DefaultConfig()
//	cfg.RelayPort = 9001
//	cfg.HostSessionTickRate = 30
//
// One instance can be shared across components in the same process, *except* when DTLS is enabled, the relay requires a server-side DTLS field (certificate + key) and hosts/clients require a client-side DTLS field (trust store).
// Use separate instances when using DTLS.
type NeonConfig struct {
	// BufferSize is the UDP receive buffer size in bytes.
	BufferSize int
	// BufferPoolInitSize is reserved; stored but not used.
	BufferPoolInitSize int
	// BufferPoolMaxSize is reserved; stored but not used.
	BufferPoolMaxSize int
	// EnforceBufferSize drops datagrams that exactly fill the receive buffer (likely truncated).
	EnforceBufferSize bool
	// RelayPort is the UDP port the relay binds to.
	RelayPort int
	// RelayCleanupIntervalMs is how often stale sessions/connections are evicted (ms).
	RelayCleanupIntervalMs int
	// RelayClientTimeoutMs is how long since last activity before a peer is considered stale (ms).
	RelayClientTimeoutMs int
	// RelayMainLoopSleepMs is the sleep between relay processing loop iterations (ms).
	RelayMainLoopSleepMs int
	// MaxPendingConnections is the max clients simultaneously in the connection handshake queue.
	MaxPendingConnections int
	// MaxRateLimiters is the max number of per-source rate limiter instances; cleared entirely when exceeded.
	MaxRateLimiters int
	// MaxPacketsPerSecond is the per-source packet rate limit; excess packets are silently dropped.
	MaxPacketsPerSecond int
	// MaxClientsPerSession is the maximum connected clients per session, not counting the host.
	MaxClientsPerSession int
	// HostAckTimeoutMs is how long to wait for a SESSION_CONFIG ACK before retransmitting (ms).
	HostAckTimeoutMs int
	// HostMaxAckRetries is the max SESSION_CONFIG retransmit attempts before giving up.
	HostMaxAckRetries int
	// HostSessionTokenTimeoutMs is the reconnect token validity window, 5 minutes by default (ms).
	HostSessionTokenTimeoutMs int
	// HostGracefulShutdownTimeoutMs is how long Stop() waits for pending ACKs to drain (ms).
	HostGracefulShutdownTimeoutMs int
	// HostProcessingLoopSleepMs is the sleep between host processing loop iterations (ms).
	HostProcessingLoopSleepMs int
	// HostSessionTickRate is the tick rate advertised to clients in SESSION_CONFIG.
	HostSessionTickRate int16
	// HostSessionMaxPacketSize is the max game packet size advertised in SESSION_CONFIG (bytes).
	HostSessionMaxPacketSize int16
	// ClientConnectionTimeoutMs is the max time to wait for CONNECT_ACCEPT during handshake (ms).
	ClientConnectionTimeoutMs int
	// ClientPingIntervalMs is how often to send an auto-ping when the processing loop is running (ms).
	ClientPingIntervalMs int
	// ClientInitialReconnectDelayMs is the initial backoff delay between reconnect attempts (ms).
	ClientInitialReconnectDelayMs int
	// ClientMaxReconnectDelayMs is the maximum backoff delay between reconnect attempts (ms).
	ClientMaxReconnectDelayMs int
	// ClientMaxReconnectAttempts is the max reconnect attempts before Reconnect() returns false.
	ClientMaxReconnectAttempts int
	// ClientProcessingLoopSleepMs is the sleep between client processing loop iterations (ms).
	ClientProcessingLoopSleepMs int
	// ReliablePacketTimeoutMs is the per-packet ACK timeout before retransmit (ms).
	ReliablePacketTimeoutMs int
	// ReliablePacketMaxRetries is the max retransmit attempts before OnDeliveryFailed fires.
	ReliablePacketMaxRetries int
	// DTLS is the DTLS configuration, or nil to disable encryption.
	//
	// The relay must use a server-side DtlsConfig (with certificate + key).
	// Hosts and clients must use a client-side DtlsConfig (with trust store).
	// Do not share a single NeonConfig between the relay and a host/client when DTLS is enabled.
	DTLS *DtlsConfig
}

// DefaultConfig returns a NeonConfig populated with the defaults shared by every QTI Neon implementation.
// See CONFIGURATION.md in the repository root for the canonical reference.
func DefaultConfig() *NeonConfig {
	return &NeonConfig{
		BufferSize:         65535,
		BufferPoolInitSize: 16,
		BufferPoolMaxSize:  64,
		EnforceBufferSize:  true,

		RelayPort:              7777,
		RelayCleanupIntervalMs: 5000,
		RelayClientTimeoutMs:   15000,
		RelayMainLoopSleepMs:   1,
		MaxPendingConnections:  64,
		MaxRateLimiters:        1024,
		MaxPacketsPerSecond:    100,
		MaxClientsPerSession:   32,

		HostAckTimeoutMs:              2000,
		HostMaxAckRetries:             5,
		HostSessionTokenTimeoutMs:     300_000,
		HostGracefulShutdownTimeoutMs: 3000,
		HostProcessingLoopSleepMs:     10,
		HostSessionTickRate:           60,
		HostSessionMaxPacketSize:      1200,

		ClientConnectionTimeoutMs:     5000,
		ClientPingIntervalMs:          5000,
		ClientInitialReconnectDelayMs: 1000,
		ClientMaxReconnectDelayMs:     30_000,
		ClientMaxReconnectAttempts:    6,
		ClientProcessingLoopSleepMs:   10,

		ReliablePacketTimeoutMs:  2000,
		ReliablePacketMaxRetries: 5,

		DTLS: nil,
	}
}

// IsDTLSEnabled reports whether a DtlsConfig has been set.
func (c *NeonConfig) IsDTLSEnabled() bool {
	return c.DTLS != nil
}

func requirePositive(v int, name string) error {
	if v <= 0 {
		return fmt.Errorf("%s must be > 0", name)
	}
	return nil
}

func requireNonNegative(v int, name string) error {
	if v < 0 {
		return fmt.Errorf("%s must be >= 0", name)
	}
	return nil
}

// validate checks that every field holds a legal value, returning the first violation encountered.
func (c *NeonConfig) validate() error {
	if c.BufferSize < 8 {
		return fmt.Errorf("BufferSize must be >= 8")
	}
	if err := requirePositive(c.BufferPoolInitSize, "BufferPoolInitSize"); err != nil {
		return err
	}
	if c.BufferPoolMaxSize < c.BufferPoolInitSize {
		return fmt.Errorf("BufferPoolMaxSize must be >= BufferPoolInitSize")
	}

	if err := requireNonNegative(c.RelayCleanupIntervalMs, "RelayCleanupIntervalMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.RelayClientTimeoutMs, "RelayClientTimeoutMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.RelayMainLoopSleepMs, "RelayMainLoopSleepMs"); err != nil {
		return err
	}
	if err := requirePositive(c.MaxPendingConnections, "MaxPendingConnections"); err != nil {
		return err
	}
	if err := requirePositive(c.MaxRateLimiters, "MaxRateLimiters"); err != nil {
		return err
	}
	if err := requirePositive(c.MaxPacketsPerSecond, "MaxPacketsPerSecond"); err != nil {
		return err
	}
	if err := requirePositive(c.MaxClientsPerSession, "MaxClientsPerSession"); err != nil {
		return err
	}

	if err := requireNonNegative(c.HostAckTimeoutMs, "HostAckTimeoutMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.HostMaxAckRetries, "HostMaxAckRetries"); err != nil {
		return err
	}
	if err := requireNonNegative(c.HostSessionTokenTimeoutMs, "HostSessionTokenTimeoutMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.HostGracefulShutdownTimeoutMs, "HostGracefulShutdownTimeoutMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.HostProcessingLoopSleepMs, "HostProcessingLoopSleepMs"); err != nil {
		return err
	}
	if c.HostSessionTickRate <= 0 {
		return fmt.Errorf("HostSessionTickRate must be > 0")
	}
	if c.HostSessionMaxPacketSize <= 0 {
		return fmt.Errorf("HostSessionMaxPacketSize must be > 0")
	}

	if err := requireNonNegative(c.ClientConnectionTimeoutMs, "ClientConnectionTimeoutMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.ClientPingIntervalMs, "ClientPingIntervalMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.ClientInitialReconnectDelayMs, "ClientInitialReconnectDelayMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.ClientMaxReconnectDelayMs, "ClientMaxReconnectDelayMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.ClientMaxReconnectAttempts, "ClientMaxReconnectAttempts"); err != nil {
		return err
	}
	if err := requireNonNegative(c.ClientProcessingLoopSleepMs, "ClientProcessingLoopSleepMs"); err != nil {
		return err
	}

	if err := requireNonNegative(c.ReliablePacketTimeoutMs, "ReliablePacketTimeoutMs"); err != nil {
		return err
	}
	if err := requireNonNegative(c.ReliablePacketMaxRetries, "ReliablePacketMaxRetries"); err != nil {
		return err
	}

	return nil
}
