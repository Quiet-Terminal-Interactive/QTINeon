package qtineon

import (
	"net"
	"sync"
	"time"
)

type reliablePendingEntry struct {
	packet  NeonPacket
	sentAt  time.Time
	retries int
}

// ReliablePacketManager provides opt-in reliable delivery with retransmit and duplicate detection.
//
// Tracks outbound packets waiting for ACK, retransmits on timeout, and deduplicates inbound packets per sender using signed 16-bit sequence arithmetic, correctly handling the 65535 -> 0 wrap-around.
//
// Call ProcessRetransmissions periodically (e.g. from the host processing loop) to drive retransmit and failure logic.
//
//	mgr := qtineon.NewReliablePacketManager(socket, relayAddr, clientID, cfg)
//	mgr.SendReliable(packet)
//
//	// In the processing loop:
//	mgr.ProcessRetransmissions()
//
//	// On receiving an Ack:
//	for _, seq := range ack.Sequences {
//		mgr.Acknowledge(seq)
//	}
type ReliablePacketManager struct {
	socket      *neonSocket
	destination net.UDPAddr
	clientID    uint8
	timeout     time.Duration
	maxRetries  int

	mu              sync.Mutex
	pending         map[uint16]reliablePendingEntry
	lastReceivedSeq map[uint8]uint16
	ackSequence     uint16

	onDeliveryFailed func(uint16)
}

// NewReliablePacketManager creates a ReliablePacketManager that sends to destination on behalf of clientID.
func NewReliablePacketManager(socket *neonSocket, destination net.UDPAddr, clientID uint8, cfg *NeonConfig) *ReliablePacketManager {
	return &ReliablePacketManager{
		socket:          socket,
		destination:     destination,
		clientID:        clientID,
		timeout:         time.Duration(cfg.ReliablePacketTimeoutMs) * time.Millisecond,
		maxRetries:      cfg.ReliablePacketMaxRetries,
		pending:         make(map[uint16]reliablePendingEntry),
		lastReceivedSeq: make(map[uint8]uint16),
	}
}

// SendReliable sends packet and tracks it for retransmission until ACKed.
func (m *ReliablePacketManager) SendReliable(packet NeonPacket) error {
	seq := packet.Header.Sequence
	if err := m.socket.sendPacket(packet, m.destination); err != nil {
		return err
	}
	m.mu.Lock()
	m.pending[seq] = reliablePendingEntry{packet, time.Now(), 0}
	m.mu.Unlock()
	return nil
}

// Acknowledge marks sequence as acknowledged, removing it from the retransmit queue.
func (m *ReliablePacketManager) Acknowledge(sequence uint16) {
	m.mu.Lock()
	delete(m.pending, sequence)
	m.mu.Unlock()
}

// ProcessRetransmissions checks all pending packets for timeout, retransmitting or failing as appropriate.
func (m *ReliablePacketManager) ProcessRetransmissions() error {
	now := time.Now()

	m.mu.Lock()
	var toRetry, toFail []uint16
	for seq, entry := range m.pending {
		if now.Sub(entry.sentAt) >= m.timeout {
			if entry.retries >= m.maxRetries {
				toFail = append(toFail, seq)
			} else {
				toRetry = append(toRetry, seq)
			}
		}
	}
	for _, seq := range toFail {
		delete(m.pending, seq)
	}
	retryPackets := make([]NeonPacket, 0, len(toRetry))
	for _, seq := range toRetry {
		entry := m.pending[seq]
		m.pending[seq] = reliablePendingEntry{entry.packet, now, entry.retries + 1}
		retryPackets = append(retryPackets, entry.packet)
	}
	callback := m.onDeliveryFailed
	m.mu.Unlock()

	for _, seq := range toFail {
		if callback != nil {
			callback(seq)
		}
	}
	for _, packet := range retryPackets {
		if err := m.socket.sendPacket(packet, m.destination); err != nil {
			return err
		}
	}
	return nil
}

// IsDuplicate reports whether sequence from senderID is a duplicate.
//
// Uses signed 16-bit subtraction to handle the 65535 -> 0 wrap-around.
// Updates the last-seen sequence for the sender on non-duplicate packets.
func (m *ReliablePacketManager) IsDuplicate(senderID uint8, sequence uint16) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	last, ok := m.lastReceivedSeq[senderID]
	if ok && signed16(int(sequence)-int(last)) <= 0 {
		return true
	}
	m.lastReceivedSeq[senderID] = sequence
	return false
}

// SendAckFor sends an Ack for sequence.
func (m *ReliablePacketManager) SendAckFor(sequence uint16) error {
	m.mu.Lock()
	seq := m.ackSequence
	m.ackSequence++
	m.mu.Unlock()

	packet := NewNeonPacket(AckType, seq, m.clientID, 0, Ack{Sequences: []uint16{sequence}})
	return m.socket.sendPacket(packet, m.destination)
}

// HasPending reports whether any packets are still awaiting acknowledgement.
func (m *ReliablePacketManager) HasPending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending) > 0
}

// SetOnDeliveryFailed sets a callback fired with the sequence number when delivery fails.
//
// The callback is invoked from the goroutine calling ProcessRetransmissions.
func (m *ReliablePacketManager) SetOnDeliveryFailed(callback func(uint16)) {
	m.mu.Lock()
	m.onDeliveryFailed = callback
	m.mu.Unlock()
}
