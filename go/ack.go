package qtineon

import "time"

type ackEntry struct {
	packet  NeonPacket
	sentAt  time.Time
	retries int
}

// ackProcessResult is the result of one ackStateMachine.process tick.
type ackProcessResult struct {
	// needsRetry holds sequence numbers whose timeout elapsed and that should be retransmitted.
	needsRetry []uint16
	// failed holds sequence numbers that have exhausted all retries.
	failed []uint16
}

// ackStateMachine tracks outbound SESSION_CONFIG packets awaiting acknowledgement.
//
// Used internally by NeonHost.
// Call process on each host loop iteration to drive retransmit logic.
type ackStateMachine struct {
	timeout    time.Duration
	maxRetries int
	pending    map[uint16]ackEntry
}

func newAckStateMachine(ackTimeoutMs, maxRetries int) *ackStateMachine {
	return &ackStateMachine{
		timeout:    time.Duration(ackTimeoutMs) * time.Millisecond,
		maxRetries: maxRetries,
		pending:    make(map[uint16]ackEntry),
	}
}

// track begins tracking packet for retransmission until ACKed.
func (a *ackStateMachine) track(sequence uint16, packet NeonPacket) {
	a.pending[sequence] = ackEntry{packet, time.Now(), 0}
}

// process evaluates pending entries and returns which need a retry or have failed.
func (a *ackStateMachine) process() ackProcessResult {
	now := time.Now()
	var result ackProcessResult
	for seq, entry := range a.pending {
		if now.Sub(entry.sentAt) >= a.timeout {
			if entry.retries >= a.maxRetries {
				result.failed = append(result.failed, seq)
			} else {
				result.needsRetry = append(result.needsRetry, seq)
			}
		}
	}
	return result
}

// markResent records a retransmission attempt for sequence.
//
// Returns the packet to retransmit, and whether sequence is still tracked.
func (a *ackStateMachine) markResent(sequence uint16) (NeonPacket, bool) {
	entry, ok := a.pending[sequence]
	if !ok {
		return NeonPacket{}, false
	}
	a.pending[sequence] = ackEntry{entry.packet, time.Now(), entry.retries + 1}
	return entry.packet, true
}

// acknowledge removes sequence from the pending set.
func (a *ackStateMachine) acknowledge(sequence uint16) {
	delete(a.pending, sequence)
}

// getPacket returns the tracked packet for sequence, and whether it exists.
func (a *ackStateMachine) getPacket(sequence uint16) (NeonPacket, bool) {
	entry, ok := a.pending[sequence]
	return entry.packet, ok
}

// hasPending reports whether any packets are still awaiting acknowledgement.
func (a *ackStateMachine) hasPending() bool {
	return len(a.pending) > 0
}
