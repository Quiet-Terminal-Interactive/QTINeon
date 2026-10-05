package qtineon

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type rawPacket struct {
	data []byte
	addr net.UDPAddr
}

// parseAddress resolves a "host:port" address string to a net.UDPAddr.
func parseAddress(address string) (net.UDPAddr, error) {
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return net.UDPAddr{}, err
	}
	return *addr, nil
}

// neonSocket is a UDP socket wrapper with optional per-peer DTLS encryption.
//
// In plaintext mode it is a thin wrapper around *net.UDPConn.
// When DTLS is enabled, each peer address gets its own piondtls.Conn multiplexed over the shared socket via a pipeConn shim, the Go analogue of the per-peer SSLEngine/BIO architecture used by the Java/Python implementations.
//
// A single background goroutine continuously drains the underlying *net.UDPConn and classifies each datagram as a plaintext Neon packet or a DTLS record; this also drives pion's internal handshake/retransmit timers, which need continuous reading regardless of whether a caller is actively polling.
type neonSocket struct {
	conn       *net.UDPConn
	bufferSize int
	enforce    bool

	inbound chan rawPacket
	closed  atomic.Bool

	mu           sync.Mutex
	dtlsConfig   *DtlsConfig
	isDTLSServer bool
	dtlsSessions map[string]*dtlsSession
	pendingPipes map[string]*pipeConn
}

func newNeonSocket(bindAddr *net.UDPAddr, cfg *NeonConfig) (*neonSocket, error) {
	conn, err := net.ListenUDP("udp4", bindAddr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadBuffer(cfg.BufferSize)

	s := &neonSocket{
		conn:         conn,
		bufferSize:   cfg.BufferSize,
		enforce:      cfg.EnforceBufferSize,
		inbound:      make(chan rawPacket, 1024),
		dtlsSessions: make(map[string]*dtlsSession),
		pendingPipes: make(map[string]*pipeConn),
	}
	go s.readLoop()
	return s, nil
}

func (s *neonSocket) readLoop() {
	buf := make([]byte, s.bufferSize)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if s.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if n == 0 {
			continue
		}
		if s.enforce && n == s.bufferSize {
			continue
		}

		data := buf[:n]
		if isDTLSRecord(data[0]) {
			cp := make([]byte, n)
			copy(cp, data)
			s.handleDTLS(cp, *addr)
			continue
		}

		if n < HeaderSize {
			continue
		}
		cp := make([]byte, n)
		copy(cp, data)
		s.inbound <- rawPacket{cp, *addr}
	}
}

func (s *neonSocket) handleDTLS(data []byte, addr net.UDPAddr) {
	key := addr.String()

	s.mu.Lock()
	if session, ok := s.dtlsSessions[key]; ok {
		s.mu.Unlock()
		session.pipe.push(data)
		return
	}
	if pipe, ok := s.pendingPipes[key]; ok {
		s.mu.Unlock()
		pipe.push(data)
		return
	}
	if !s.isDTLSServer || s.dtlsConfig == nil {
		s.mu.Unlock()
		return
	}

	addrCopy := addr
	pipe := newPipeConn(s.conn.LocalAddr(), &addrCopy, func(b []byte) (int, error) {
		return s.conn.WriteToUDP(b, &addrCopy)
	})
	s.pendingPipes[key] = pipe
	cfg := s.dtlsConfig
	s.mu.Unlock()

	pipe.push(data)

	go func() {
		conn, err := handshakeServer(pipe, cfg, 30*time.Second)

		s.mu.Lock()
		delete(s.pendingPipes, key)
		if err != nil {
			s.mu.Unlock()
			pipe.Close()
			return
		}
		s.dtlsSessions[key] = &dtlsSession{pipe: pipe, conn: conn}
		s.mu.Unlock()

		s.readSessionLoop(key, addrCopy, conn)
	}()
}

func (s *neonSocket) readSessionLoop(key string, addr net.UDPAddr, conn interface {
	Read([]byte) (int, error)
}) {
	buf := make([]byte, s.bufferSize)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			s.mu.Lock()
			delete(s.dtlsSessions, key)
			s.mu.Unlock()
			return
		}
		if n < HeaderSize {
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		s.inbound <- rawPacket{data, addr}
	}
}

// enableServerDTLS configures server-side DTLS.
//
// Called by the relay before entering its processing loop.
// Inbound DTLS handshakes from new peers are initiated automatically when a DTLS record arrives from an unknown address.
func (s *neonSocket) enableServerDTLS(cfg *DtlsConfig) {
	s.mu.Lock()
	s.dtlsConfig = cfg
	s.isDTLSServer = true
	s.mu.Unlock()
}

// performClientHandshake performs a DTLS client-side handshake with peer, blocking until it completes or timeout elapses.
func (s *neonSocket) performClientHandshake(cfg *DtlsConfig, peer net.UDPAddr, timeout time.Duration) error {
	key := peer.String()
	peerCopy := peer
	pipe := newPipeConn(s.conn.LocalAddr(), &peerCopy, func(b []byte) (int, error) {
		return s.conn.WriteToUDP(b, &peerCopy)
	})

	s.mu.Lock()
	s.pendingPipes[key] = pipe
	s.mu.Unlock()

	conn, err := handshakeClient(pipe, cfg, timeout)

	s.mu.Lock()
	delete(s.pendingPipes, key)
	if err != nil {
		s.mu.Unlock()
		pipe.Close()
		return err
	}
	s.dtlsSessions[key] = &dtlsSession{pipe: pipe, conn: conn}
	s.mu.Unlock()

	go s.readSessionLoop(key, peerCopy, conn)
	return nil
}

// removeDTLSSession discards the DTLS session for addr when a peer disconnects.
func (s *neonSocket) removeDTLSSession(addr net.UDPAddr) {
	key := addr.String()
	s.mu.Lock()
	session, ok := s.dtlsSessions[key]
	delete(s.dtlsSessions, key)
	s.mu.Unlock()
	if ok {
		_ = session.conn.Close()
	}
}

// send sends data to address, encrypting via DTLS if a session exists.
func (s *neonSocket) send(data []byte, address net.UDPAddr) error {
	s.mu.Lock()
	session, ok := s.dtlsSessions[address.String()]
	s.mu.Unlock()
	if ok {
		_, err := session.conn.Write(data)
		return err
	}
	_, err := s.conn.WriteToUDP(data, &address)
	return err
}

// sendPacket serialises packet and sends it to address.
func (s *neonSocket) sendPacket(packet NeonPacket, address net.UDPAddr) error {
	data, err := packet.ToBytes()
	if err != nil {
		return err
	}
	return s.send(data, address)
}

// receive returns the next available (data, address) pair without blocking.
func (s *neonSocket) receive() ([]byte, net.UDPAddr, bool) {
	select {
	case pkt := <-s.inbound:
		return pkt.data, pkt.addr, true
	default:
		return nil, net.UDPAddr{}, false
	}
}

// receiveTimeout returns the next available (data, address) pair, blocking up to timeout.
func (s *neonSocket) receiveTimeout(timeout time.Duration) ([]byte, net.UDPAddr, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case pkt := <-s.inbound:
		return pkt.data, pkt.addr, true
	case <-timer.C:
		return nil, net.UDPAddr{}, false
	}
}

// receivePacket returns the next parsed (NeonPacket, address) pair without blocking.
// A malformed datagram is silently dropped (ok=false), matching the other implementations' tolerant-receive behaviour.
func (s *neonSocket) receivePacket() (NeonPacket, net.UDPAddr, bool) {
	data, addr, ok := s.receive()
	if !ok {
		return NeonPacket{}, net.UDPAddr{}, false
	}
	packet, err := NeonPacketFromBytes(data)
	if err != nil {
		return NeonPacket{}, net.UDPAddr{}, false
	}
	return packet, addr, true
}

// receivePacketTimeout returns the next parsed (NeonPacket, address) pair, blocking up to timeout.
func (s *neonSocket) receivePacketTimeout(timeout time.Duration) (NeonPacket, net.UDPAddr, bool) {
	data, addr, ok := s.receiveTimeout(timeout)
	if !ok {
		return NeonPacket{}, net.UDPAddr{}, false
	}
	packet, err := NeonPacketFromBytes(data)
	if err != nil {
		return NeonPacket{}, net.UDPAddr{}, false
	}
	return packet, addr, true
}

// localAddr returns the bound local address.
func (s *neonSocket) localAddr() *net.UDPAddr {
	return s.conn.LocalAddr().(*net.UDPAddr)
}

// isClosed reports whether close has been called.
func (s *neonSocket) isClosed() bool {
	return s.closed.Load()
}

// close closes the underlying socket and any DTLS sessions.
func (s *neonSocket) close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.mu.Lock()
	for _, session := range s.dtlsSessions {
		_ = session.conn.Close()
	}
	s.dtlsSessions = make(map[string]*dtlsSession)
	for _, pipe := range s.pendingPipes {
		pipe.Close()
	}
	s.pendingPipes = make(map[string]*pipeConn)
	s.mu.Unlock()
	return s.conn.Close()
}
