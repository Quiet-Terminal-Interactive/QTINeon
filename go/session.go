package qtineon

import (
	"net"
	"time"
)

type session struct {
	peers        map[uint8]net.UDPAddr
	lastActivity time.Time
}

func newSession() *session {
	return &session{
		peers:        make(map[uint8]net.UDPAddr),
		lastActivity: time.Now(),
	}
}

// sessionManager stores host and client address mappings per session.
//
// All methods are called exclusively from the relay processing goroutine and are therefore not locked.
type sessionManager struct {
	sessions       map[int32]*session
	peerToSession  map[string]int32
	peerToClientID map[string]uint8
}

func newSessionManager() *sessionManager {
	return &sessionManager{
		sessions:       make(map[int32]*session),
		peerToSession:  make(map[string]int32),
		peerToClientID: make(map[string]uint8),
	}
}

// registerHost creates a new session with addr as the host (client ID 1).
func (m *sessionManager) registerHost(sessionID int32, addr net.UDPAddr) {
	s := newSession()
	s.peers[1] = addr
	m.sessions[sessionID] = s
	m.peerToSession[addr.String()] = sessionID
	m.peerToClientID[addr.String()] = 1
}

// registerPeer adds a new peer to an existing session.
func (m *sessionManager) registerPeer(sessionID int32, clientID uint8, addr net.UDPAddr) {
	s, ok := m.sessions[sessionID]
	if !ok {
		return
	}
	s.peers[clientID] = addr
	m.peerToSession[addr.String()] = sessionID
	m.peerToClientID[addr.String()] = clientID
	s.lastActivity = time.Now()
}

// removePeer removes addr from its session; tears down the whole session if it was the host.
func (m *sessionManager) removePeer(addr net.UDPAddr) {
	key := addr.String()
	sessionID, ok := m.peerToSession[key]
	if !ok {
		return
	}
	delete(m.peerToSession, key)
	clientID, ok := m.peerToClientID[key]
	if !ok {
		return
	}
	delete(m.peerToClientID, key)
	s, ok := m.sessions[sessionID]
	if !ok {
		return
	}
	delete(s.peers, clientID)
	if clientID == 1 {
		for _, peerAddr := range s.peers {
			delete(m.peerToSession, peerAddr.String())
			delete(m.peerToClientID, peerAddr.String())
		}
		delete(m.sessions, sessionID)
	}
}

// getHost returns the host address for sessionID, and whether it exists.
func (m *sessionManager) getHost(sessionID int32) (net.UDPAddr, bool) {
	s, ok := m.sessions[sessionID]
	if !ok {
		return net.UDPAddr{}, false
	}
	addr, ok := s.peers[1]
	return addr, ok
}

// getPeerAddress returns the address of clientID in sessionID, and whether it exists.
func (m *sessionManager) getPeerAddress(sessionID int32, clientID uint8) (net.UDPAddr, bool) {
	s, ok := m.sessions[sessionID]
	if !ok {
		return net.UDPAddr{}, false
	}
	addr, ok := s.peers[clientID]
	return addr, ok
}

// getAllPeersExcept returns all peer addresses in sessionID except exclude.
func (m *sessionManager) getAllPeersExcept(sessionID int32, exclude net.UDPAddr) []net.UDPAddr {
	s, ok := m.sessions[sessionID]
	if !ok {
		return nil
	}
	excludeKey := exclude.String()
	result := make([]net.UDPAddr, 0, len(s.peers))
	for _, addr := range s.peers {
		if addr.String() != excludeKey {
			result = append(result, addr)
		}
	}
	return result
}

// getSessionForPeer returns the session ID that addr belongs to, and whether it exists.
func (m *sessionManager) getSessionForPeer(addr net.UDPAddr) (int32, bool) {
	sessionID, ok := m.peerToSession[addr.String()]
	return sessionID, ok
}

// getClientCount returns the number of connected clients (not counting the host).
func (m *sessionManager) getClientCount(sessionID int32) int {
	s, ok := m.sessions[sessionID]
	if !ok {
		return 0
	}
	n := len(s.peers) - 1
	if n < 0 {
		return 0
	}
	return n
}

// updateLastSeen refreshes the last-activity timestamp for the session owning addr.
func (m *sessionManager) updateLastSeen(addr net.UDPAddr) {
	sessionID, ok := m.peerToSession[addr.String()]
	if !ok {
		return
	}
	if s, ok := m.sessions[sessionID]; ok {
		s.lastActivity = time.Now()
	}
}

// updatePeerAddress remaps clientID in sessionID to newAddr (used during reconnect).
func (m *sessionManager) updatePeerAddress(sessionID int32, clientID uint8, newAddr net.UDPAddr) {
	s, ok := m.sessions[sessionID]
	if !ok {
		return
	}
	if oldAddr, ok := s.peers[clientID]; ok {
		delete(m.peerToSession, oldAddr.String())
		delete(m.peerToClientID, oldAddr.String())
	}
	s.peers[clientID] = newAddr
	m.peerToSession[newAddr.String()] = sessionID
	m.peerToClientID[newAddr.String()] = clientID
}

// removeStale evicts sessions whose last activity exceeds thresholdMs milliseconds.
func (m *sessionManager) removeStale(thresholdMs int) {
	now := time.Now()
	threshold := time.Duration(thresholdMs) * time.Millisecond
	stale := make([]int32, 0)
	for sid, s := range m.sessions {
		if now.Sub(s.lastActivity) > threshold {
			stale = append(stale, sid)
		}
	}
	for _, sid := range stale {
		s := m.sessions[sid]
		delete(m.sessions, sid)
		for _, addr := range s.peers {
			delete(m.peerToSession, addr.String())
			delete(m.peerToClientID, addr.String())
		}
	}
}
