package qtineon

import (
	"net"
	"testing"
	"time"
)

func newReliableTestSockets(t *testing.T) (*neonSocket, *neonSocket) {
	t.Helper()
	cfg := DefaultConfig()
	sender, err := newNeonSocket(&net.UDPAddr{}, cfg)
	if err != nil {
		t.Fatalf("newNeonSocket (sender): %v", err)
	}
	receiver, err := newNeonSocket(&net.UDPAddr{}, cfg)
	if err != nil {
		t.Fatalf("newNeonSocket (receiver): %v", err)
	}
	t.Cleanup(func() {
		_ = sender.close()
		_ = receiver.close()
	})
	return sender, receiver
}

func drainCount(sock *neonSocket) int {
	time.Sleep(30 * time.Millisecond)
	count := 0
	for {
		_, _, ok := sock.receive()
		if !ok {
			break
		}
		count++
	}
	return count
}

func newTestReliableManager(t *testing.T, timeoutMs, maxRetries int) (*ReliablePacketManager, *neonSocket) {
	t.Helper()
	sender, receiver := newReliableTestSockets(t)
	cfg := DefaultConfig()
	cfg.ReliablePacketTimeoutMs = timeoutMs
	cfg.ReliablePacketMaxRetries = maxRetries
	mgr := NewReliablePacketManager(sender, *receiver.localAddr(), 2, cfg)
	return mgr, receiver
}

func makeReliableTestPacket(seq uint16) NeonPacket {
	return NewNeonPacket(PingType, seq, 2, 1, Ping{Timestamp: 0})
}

func TestReliableSendRecordsPending(t *testing.T) {
	mgr, receiver := newTestReliableManager(t, 200, 3)
	if err := mgr.SendReliable(makeReliableTestPacket(5)); err != nil {
		t.Fatalf("SendReliable: %v", err)
	}
	if got := drainCount(receiver); got != 1 {
		t.Errorf("received %d packets, want 1", got)
	}
	if !mgr.HasPending() {
		t.Error("HasPending() = false, want true")
	}
}

func TestReliableAcknowledgeClearsPending(t *testing.T) {
	mgr, _ := newTestReliableManager(t, 200, 3)
	if err := mgr.SendReliable(makeReliableTestPacket(5)); err != nil {
		t.Fatalf("SendReliable: %v", err)
	}
	mgr.Acknowledge(5)
	if mgr.HasPending() {
		t.Error("HasPending() = true, want false")
	}
}

func TestReliableRetransmitOnTimeout(t *testing.T) {
	mgr, receiver := newTestReliableManager(t, 50, 3)
	if err := mgr.SendReliable(makeReliableTestPacket(1)); err != nil {
		t.Fatalf("SendReliable: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if err := mgr.ProcessRetransmissions(); err != nil {
		t.Fatalf("ProcessRetransmissions: %v", err)
	}
	if got := drainCount(receiver); got != 2 {
		t.Errorf("received %d packets, want 2 (original + 1 retransmit)", got)
	}
}

func TestReliableDeliveryFailedCallback(t *testing.T) {
	mgr, _ := newTestReliableManager(t, 20, 1)
	var failed []uint16
	mgr.SetOnDeliveryFailed(func(seq uint16) { failed = append(failed, seq) })

	if err := mgr.SendReliable(makeReliableTestPacket(7)); err != nil {
		t.Fatalf("SendReliable: %v", err)
	}

	time.Sleep(25 * time.Millisecond)
	if err := mgr.ProcessRetransmissions(); err != nil {
		t.Fatalf("ProcessRetransmissions: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	if err := mgr.ProcessRetransmissions(); err != nil {
		t.Fatalf("ProcessRetransmissions: %v", err)
	}

	found := false
	for _, seq := range failed {
		if seq == 7 {
			found = true
		}
	}
	if !found {
		t.Errorf("failed = %v, want to contain 7", failed)
	}
	if mgr.HasPending() {
		t.Error("HasPending() = true, want false")
	}
}

func TestReliableNoRetransmitAfterAck(t *testing.T) {
	mgr, receiver := newTestReliableManager(t, 30, 3)
	if err := mgr.SendReliable(makeReliableTestPacket(3)); err != nil {
		t.Fatalf("SendReliable: %v", err)
	}
	mgr.Acknowledge(3)
	time.Sleep(50 * time.Millisecond)
	if err := mgr.ProcessRetransmissions(); err != nil {
		t.Fatalf("ProcessRetransmissions: %v", err)
	}
	if got := drainCount(receiver); got != 1 {
		t.Errorf("received %d packets, want 1 (only the original send)", got)
	}
}

func TestReliableIsDuplicateDetectsDuplicate(t *testing.T) {
	mgr, _ := newTestReliableManager(t, 200, 3)
	if mgr.IsDuplicate(2, 10) {
		t.Error("first sighting of sequence 10 should not be a duplicate")
	}
	if !mgr.IsDuplicate(2, 10) {
		t.Error("repeated sequence 10 should be a duplicate")
	}
	if !mgr.IsDuplicate(2, 9) {
		t.Error("older sequence 9 should be a duplicate")
	}
}

func TestReliableIsDuplicateDifferentSendersIndependent(t *testing.T) {
	mgr, _ := newTestReliableManager(t, 200, 3)
	if mgr.IsDuplicate(2, 5) {
		t.Error("first sighting for sender 2 should not be a duplicate")
	}
	if mgr.IsDuplicate(3, 5) {
		t.Error("first sighting for sender 3 should not be a duplicate")
	}
}

func TestReliableIsDuplicateWrapAround(t *testing.T) {
	mgr, _ := newTestReliableManager(t, 200, 3)
	mgr.IsDuplicate(2, 65530)
	if mgr.IsDuplicate(2, 1) {
		t.Error("sequence 1 after wrap-around should not be a duplicate")
	}
}

func TestReliableSendAckFor(t *testing.T) {
	mgr, receiver := newTestReliableManager(t, 200, 3)
	if err := mgr.SendAckFor(42); err != nil {
		t.Fatalf("SendAckFor: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	packet, _, ok := receiver.receivePacket()
	if !ok {
		t.Fatal("expected to receive an ACK packet")
	}
	ptype, err := PacketTypeFromByte(packet.Header.PacketType)
	if err != nil {
		t.Fatalf("PacketTypeFromByte: %v", err)
	}
	if ptype != AckType {
		t.Errorf("packet type = %v, want AckType", ptype)
	}
	ack, ok := packet.Payload.(Ack)
	if !ok {
		t.Fatalf("payload type = %T, want Ack", packet.Payload)
	}
	found := false
	for _, seq := range ack.Sequences {
		if seq == 42 {
			found = true
		}
	}
	if !found {
		t.Errorf("Sequences = %v, want to contain 42", ack.Sequences)
	}
}
