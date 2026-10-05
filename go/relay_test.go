package qtineon

import (
	"net"
	"testing"
	"time"
)

func freeUDPPort(t *testing.T) int {
	t.Helper()
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP (probe): %v", err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	return port
}

func newTestRelay(t *testing.T, configure func(*NeonConfig)) *NeonRelay {
	t.Helper()
	cfg := DefaultConfig()
	cfg.RelayPort = freeUDPPort(t)
	if configure != nil {
		configure(cfg)
	}
	relay, err := NewRelay("127.0.0.1", cfg)
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	go func() {
		_ = relay.StartAndRun()
	}()
	time.Sleep(50 * time.Millisecond)
	t.Cleanup(func() {
		if relay.IsRunning() {
			_ = relay.Stop()
		}
	})
	return relay
}

func newRawTestConn(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func sendPacket(t *testing.T, conn *net.UDPConn, packet NeonPacket, dest *net.UDPAddr) {
	t.Helper()
	data, err := packet.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
	if _, err := conn.WriteToUDP(data, dest); err != nil {
		t.Fatalf("WriteToUDP: %v", err)
	}
}

func recvPacket(t *testing.T, conn *net.UDPConn, timeout time.Duration) (NeonPacket, bool) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 65535)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return NeonPacket{}, false
	}
	packet, err := NeonPacketFromBytes(buf[:n])
	if err != nil {
		t.Fatalf("NeonPacketFromBytes: %v", err)
	}
	return packet, true
}

func TestRelayHostRegisterReturnsConnectAccept(t *testing.T) {
	relay := newTestRelay(t, nil)
	addr := relay.LocalAddress()
	conn := newRawTestConn(t)

	req := NewNeonPacket(HostRegisterType, 0, 1, 0, HostRegister{SessionID: 1, HostToken: 0})
	sendPacket(t, conn, req, addr)

	resp, ok := recvPacket(t, conn, time.Second)
	if !ok {
		t.Fatal("expected a response from the relay")
	}
	ptype, err := PacketTypeFromByte(resp.Header.PacketType)
	if err != nil {
		t.Fatalf("PacketTypeFromByte: %v", err)
	}
	if ptype != ConnectAcceptType {
		t.Errorf("packet type = %v, want ConnectAcceptType", ptype)
	}
	accept, ok := resp.Payload.(ConnectAccept)
	if !ok {
		t.Fatalf("payload type = %T, want ConnectAccept", resp.Payload)
	}
	if accept.ClientID != 1 {
		t.Errorf("ClientID = %d, want 1", accept.ClientID)
	}
}

func TestRelayInvalidSessionIDIgnored(t *testing.T) {
	relay := newTestRelay(t, nil)
	addr := relay.LocalAddress()
	conn := newRawTestConn(t)

	req := NewNeonPacket(HostRegisterType, 0, 1, 0, HostRegister{SessionID: 0, HostToken: 0})
	sendPacket(t, conn, req, addr)

	if _, ok := recvPacket(t, conn, 300*time.Millisecond); ok {
		t.Error("expected no response for invalid session ID")
	}
}

func TestRelayConnectToUnknownSessionReturnsDeny(t *testing.T) {
	relay := newTestRelay(t, nil)
	addr := relay.LocalAddress()
	conn := newRawTestConn(t)

	req := NewNeonPacket(ConnectRequestType, 0, 0, 1, ConnectRequest{ClientVersion: 1, Name: "bob", SessionID: 99, GameID: 0})
	sendPacket(t, conn, req, addr)

	resp, ok := recvPacket(t, conn, time.Second)
	if !ok {
		t.Fatal("expected a response from the relay")
	}
	ptype, err := PacketTypeFromByte(resp.Header.PacketType)
	if err != nil {
		t.Fatalf("PacketTypeFromByte: %v", err)
	}
	if ptype != ConnectDenyType {
		t.Errorf("packet type = %v, want ConnectDenyType", ptype)
	}
	deny, ok := resp.Payload.(ConnectDeny)
	if !ok {
		t.Fatalf("payload type = %T, want ConnectDeny", resp.Payload)
	}
	if deny.Reason == "" {
		t.Error("expected a non-empty deny reason")
	}
}

func TestRelayFullConnectHandshake(t *testing.T) {
	relay := newTestRelay(t, nil)
	addr := relay.LocalAddress()
	hostConn := newRawTestConn(t)
	clientConn := newRawTestConn(t)

	// 1. Register host.
	sendPacket(t, hostConn, NewNeonPacket(HostRegisterType, 0, 1, 0, HostRegister{SessionID: 7, HostToken: 0xABCD}), addr)
	resp, ok := recvPacket(t, hostConn, time.Second)
	if !ok {
		t.Fatal("expected CONNECT_ACCEPT for host registration")
	}
	if ptype, _ := PacketTypeFromByte(resp.Header.PacketType); ptype != ConnectAcceptType {
		t.Fatalf("packet type = %v, want ConnectAcceptType", ptype)
	}

	// 2. Client sends CONNECT_REQUEST.
	sendPacket(t, clientConn, NewNeonPacket(ConnectRequestType, 0, 0, 1, ConnectRequest{ClientVersion: 1, Name: "alice", SessionID: 7, GameID: 0}), addr)

	// 3. Relay forwards to host.
	fwd, ok := recvPacket(t, hostConn, time.Second)
	if !ok {
		t.Fatal("expected relay to forward CONNECT_REQUEST to host")
	}
	if ptype, _ := PacketTypeFromByte(fwd.Header.PacketType); ptype != ConnectRequestType {
		t.Fatalf("packet type = %v, want ConnectRequestType", ptype)
	}

	// 4. Host sends CONNECT_ACCEPT.
	sendPacket(t, hostConn, NewNeonPacket(ConnectAcceptType, 1, 1, 0, ConnectAccept{ClientID: 2, SessionID: 7, Token: 0x1234}), addr)

	// 5. Client receives CONNECT_ACCEPT.
	final, ok := recvPacket(t, clientConn, time.Second)
	if !ok {
		t.Fatal("expected client to receive CONNECT_ACCEPT")
	}
	if ptype, _ := PacketTypeFromByte(final.Header.PacketType); ptype != ConnectAcceptType {
		t.Fatalf("packet type = %v, want ConnectAcceptType", ptype)
	}
	accept, ok := final.Payload.(ConnectAccept)
	if !ok {
		t.Fatalf("payload type = %T, want ConnectAccept", final.Payload)
	}
	if accept.ClientID != 2 {
		t.Errorf("ClientID = %d, want 2", accept.ClientID)
	}
}

func TestRelayDisconnectNoticeBroadcast(t *testing.T) {
	relay := newTestRelay(t, nil)
	addr := relay.LocalAddress()
	hostConn := newRawTestConn(t)
	clientConn := newRawTestConn(t)

	sendPacket(t, hostConn, NewNeonPacket(HostRegisterType, 0, 1, 0, HostRegister{SessionID: 55, HostToken: 0}), addr)
	if _, ok := recvPacket(t, hostConn, time.Second); !ok {
		t.Fatal("expected CONNECT_ACCEPT for host registration")
	}

	sendPacket(t, clientConn, NewNeonPacket(ConnectRequestType, 0, 0, 1, ConnectRequest{ClientVersion: 1, Name: "x", SessionID: 55, GameID: 0}), addr)
	if _, ok := recvPacket(t, hostConn, time.Second); !ok {
		t.Fatal("expected relay to forward CONNECT_REQUEST")
	}

	sendPacket(t, hostConn, NewNeonPacket(ConnectAcceptType, 1, 1, 0, ConnectAccept{ClientID: 2, SessionID: 55, Token: 0}), addr)
	if _, ok := recvPacket(t, clientConn, time.Second); !ok {
		t.Fatal("expected client to receive CONNECT_ACCEPT")
	}

	sendPacket(t, clientConn, NewNeonPacket(DisconnectNoticeType, 2, 2, 0, DisconnectNotice{}), addr)

	notice, ok := recvPacket(t, hostConn, time.Second)
	if !ok {
		t.Fatal("expected host to receive DISCONNECT_NOTICE")
	}
	if ptype, _ := PacketTypeFromByte(notice.Header.PacketType); ptype != DisconnectNoticeType {
		t.Errorf("packet type = %v, want DisconnectNoticeType", ptype)
	}
}

func TestRelayExcessPacketsDropped(t *testing.T) {
	relay := newTestRelay(t, func(cfg *NeonConfig) {
		cfg.MaxPacketsPerSecond = 5
	})
	addr := relay.LocalAddress()
	conn := newRawTestConn(t)

	pkt := NewNeonPacket(DisconnectNoticeType, 0, 0, 0, DisconnectNotice{})
	for i := 0; i < 200; i++ {
		sendPacket(t, conn, pkt, addr)
	}

	time.Sleep(50 * time.Millisecond)
	if !relay.IsRunning() {
		t.Error("relay should still be running after excess packets")
	}
}
