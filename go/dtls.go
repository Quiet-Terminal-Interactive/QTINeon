package qtineon

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	piondtls "github.com/pion/dtls/v2"
)

// dtls12Range and dtls13Range are the DTLS 1.2 content-type bytes (0x14-0x17) and DTLS 1.3 unified header range (0x20-0x3F).
// A first byte in either range identifies an incoming datagram as a DTLS record rather than a plaintext Neon packet.
func isDTLSRecord(firstByte byte) bool {
	return (firstByte >= 0x14 && firstByte <= 0x17) || (firstByte >= 0x20 && firstByte <= 0x3F)
}

// DtlsConfig holds DTLS 1.2/1.3 settings for a NeonRelay (server-side, certificate + key) or a NeonHost/NeonClient (client-side, trust store).
//
// Relay-terminated: each peer performs its own handshake with the relay.
// Encryption is transparent to game code once established.
type DtlsConfig struct {
	config *piondtls.Config
}

// FromKeyStore builds a server-side DtlsConfig from a PEM certificate and private key file.
// Used by the relay.
func FromKeyStore(certFile, keyFile string) (*DtlsConfig, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading DTLS key pair: %w", err)
	}
	return &DtlsConfig{
		config: &piondtls.Config{
			Certificates: []tls.Certificate{cert},
		},
	}, nil
}

// InsecureTrustAll builds a client-side DtlsConfig that accepts any certificate presented by the relay.
//
// For development and testing only; it accepts any certificate.
// For production, supply a DtlsConfig built from a trust store that pins the relay's certificate.
func InsecureTrustAll() *DtlsConfig {
	return &DtlsConfig{
		config: &piondtls.Config{
			InsecureSkipVerify: true,
		},
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type pipeConn struct {
	local  net.Addr
	remote net.Addr
	in     chan []byte
	write  func([]byte) (int, error)

	closeOnce sync.Once
	closed    chan struct{}

	deadlineMu sync.Mutex
	deadline   time.Time
}

func newPipeConn(local, remote net.Addr, write func([]byte) (int, error)) *pipeConn {
	return &pipeConn{
		local:  local,
		remote: remote,
		in:     make(chan []byte, 32),
		write:  write,
		closed: make(chan struct{}),
	}
}

// push enqueues an inbound datagram for this peer.
// Non-blocking, drops the datagram if the queue is full, matching UDP's unreliable-delivery semantics.
func (p *pipeConn) push(data []byte) {
	select {
	case p.in <- data:
	default:
	}
}

func (p *pipeConn) Read(b []byte) (int, error) {
	p.deadlineMu.Lock()
	dl := p.deadline
	p.deadlineMu.Unlock()

	var timeoutCh <-chan time.Time
	if !dl.IsZero() {
		d := time.Until(dl)
		if d <= 0 {
			return 0, timeoutError{}
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case data, ok := <-p.in:
		if !ok {
			return 0, io.EOF
		}
		return copy(b, data), nil
	case <-timeoutCh:
		return 0, timeoutError{}
	case <-p.closed:
		return 0, io.EOF
	}
}

func (p *pipeConn) Write(b []byte) (int, error) {
	return p.write(b)
}

func (p *pipeConn) Close() error {
	p.closeOnce.Do(func() { close(p.closed) })
	return nil
}

func (p *pipeConn) LocalAddr() net.Addr  { return p.local }
func (p *pipeConn) RemoteAddr() net.Addr { return p.remote }

func (p *pipeConn) SetDeadline(t time.Time) error {
	return p.SetReadDeadline(t)
}

func (p *pipeConn) SetReadDeadline(t time.Time) error {
	p.deadlineMu.Lock()
	p.deadline = t
	p.deadlineMu.Unlock()
	return nil
}

func (p *pipeConn) SetWriteDeadline(t time.Time) error {
	return nil
}

// dtlsSession is one peer's DTLS connection.
// By the time it is stored in a neonSocket's session map the handshake has already completed, both newClientDTLSSession and the server-side accept path block until piondtls.Client/Server returns.
type dtlsSession struct {
	pipe *pipeConn
	conn *piondtls.Conn
}

// handshakeServer performs a server-side DTLS handshake over pipe, blocking until it completes or timeout elapses.
func handshakeServer(pipe *pipeConn, cfg *DtlsConfig, timeout time.Duration) (*piondtls.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return piondtls.ServerWithContext(ctx, pipe, cfg.config)
}

// handshakeClient performs a client-side DTLS handshake over pipe, blocking until it completes or timeout elapses.
func handshakeClient(pipe *pipeConn, cfg *DtlsConfig, timeout time.Duration) (*piondtls.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return piondtls.ClientWithContext(ctx, pipe, cfg.config)
}
