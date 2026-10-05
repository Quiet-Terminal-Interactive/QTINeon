package qtineon

import (
	"net"
	"testing"
	"time"
)

func newTestHostWithRelay(t *testing.T, configure func(*NeonConfig)) (*NeonHost, *net.UDPConn) {
	t.Helper()
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	cfg := DefaultConfig()
	cfg.HostAckTimeoutMs = 500
	cfg.HostMaxAckRetries = 2
	cfg.HostProcessingLoopSleepMs = 5
	if configure != nil {
		configure(cfg)
	}

	host, err := NewHost(1, relayAddr.String(), cfg)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	hostAddr := host.LocalAddress()

	setupDone := make(chan struct{})
	go func() {
		defer close(setupDone)
		reg, ok := recvPacket(t, relayConn, 3*time.Second)
		if !ok {
			t.Error("expected HOST_REGISTER from host")
			return
		}
		if ptype, _ := PacketTypeFromByte(reg.Header.PacketType); ptype != HostRegisterType {
			t.Errorf("packet type = %v, want HostRegisterType", ptype)
		}
		accept := NewNeonPacket(ConnectAcceptType, 0, 0, 1, ConnectAccept{ClientID: 1, SessionID: 1, Token: 0})
		sendPacket(t, relayConn, accept, hostAddr)
	}()

	go func() {
		_ = host.StartAndRun()
	}()
	<-setupDone
	time.Sleep(50 * time.Millisecond)

	t.Cleanup(func() {
		if host.IsRunning() {
			_ = host.Stop()
		}
	})
	return host, relayConn
}

func TestHostRegistersOnStart(t *testing.T) {
	host, _ := newTestHostWithRelay(t, nil)
	if !host.IsRunning() {
		t.Error("host should be running after registration")
	}
}

func TestHostConnectedClientsEmptyInitially(t *testing.T) {
	host, _ := newTestHostWithRelay(t, nil)
	if got := host.ConnectedClients(); len(got) != 0 {
		t.Errorf("ConnectedClients() = %v, want empty", got)
	}
}

func TestHostAcceptClient(t *testing.T) {
	host, relayConn := newTestHostWithRelay(t, nil)
	hostAddr := host.LocalAddress()

	type connectedEntry struct {
		cid  uint8
		name string
	}
	var connected []connectedEntry
	host.SetClientConnectCallback(func(cid uint8, name string, sessionID int32) {
		connected = append(connected, connectedEntry{cid, name})
	})

	sendPacket(t, relayConn, NewNeonPacket(ConnectRequestType, 1, 0, 1, ConnectRequest{ClientVersion: 1, Name: "alice", SessionID: 1, GameID: 0}), hostAddr)

	accept, ok := recvPacket(t, relayConn, 2*time.Second)
	if !ok {
		t.Fatal("expected CONNECT_ACCEPT from host")
	}
	if ptype, _ := PacketTypeFromByte(accept.Header.PacketType); ptype != ConnectAcceptType {
		t.Fatalf("packet type = %v, want ConnectAcceptType", ptype)
	}
	acceptPayload := accept.Payload.(ConnectAccept)
	if acceptPayload.ClientID != 2 {
		t.Errorf("ClientID = %d, want 2", acceptPayload.ClientID)
	}

	time.Sleep(50 * time.Millisecond)
	if len(connected) != 1 {
		t.Fatalf("len(connected) = %d, want 1", len(connected))
	}
	if connected[0].name != "alice" {
		t.Errorf("connected[0].name = %q, want %q", connected[0].name, "alice")
	}

	cfg, ok := recvPacket(t, relayConn, 2*time.Second)
	if !ok {
		t.Fatal("expected SESSION_CONFIG from host")
	}
	if ptype, _ := PacketTypeFromByte(cfg.Header.PacketType); ptype != SessionConfigType {
		t.Errorf("packet type = %v, want SessionConfigType", ptype)
	}
}

func TestHostDenyDuplicateName(t *testing.T) {
	host, relayConn := newTestHostWithRelay(t, nil)
	hostAddr := host.LocalAddress()

	sendPacket(t, relayConn, NewNeonPacket(ConnectRequestType, 1, 0, 1, ConnectRequest{ClientVersion: 1, Name: "alice", SessionID: 1, GameID: 0}), hostAddr)
	recvPacket(t, relayConn, 2*time.Second) // CONNECT_ACCEPT
	recvPacket(t, relayConn, 2*time.Second) // SESSION_CONFIG
	recvPacket(t, relayConn, 2*time.Second) // PACKET_TYPE_REGISTRY

	sendPacket(t, relayConn, NewNeonPacket(ConnectRequestType, 2, 0, 1, ConnectRequest{ClientVersion: 1, Name: "alice", SessionID: 1, GameID: 0}), hostAddr)
	deny, ok := recvPacket(t, relayConn, 2*time.Second)
	if !ok {
		t.Fatal("expected CONNECT_DENY from host")
	}
	if ptype, _ := PacketTypeFromByte(deny.Header.PacketType); ptype != ConnectDenyType {
		t.Fatalf("packet type = %v, want ConnectDenyType", ptype)
	}
	denyPayload := deny.Payload.(ConnectDeny)
	if denyPayload.Reason != "Name taken" {
		t.Errorf("Reason = %q, want %q", denyPayload.Reason, "Name taken")
	}
}

func TestHostAckStopsRetransmit(t *testing.T) {
	host, relayConn := newTestHostWithRelay(t, nil)
	hostAddr := host.LocalAddress()

	sendPacket(t, relayConn, NewNeonPacket(ConnectRequestType, 1, 0, 1, ConnectRequest{ClientVersion: 1, Name: "bob", SessionID: 1, GameID: 0}), hostAddr)
	recvPacket(t, relayConn, 2*time.Second) // CONNECT_ACCEPT
	cfgPkt, ok := recvPacket(t, relayConn, 2*time.Second)
	if !ok {
		t.Fatal("expected SESSION_CONFIG from host")
	}
	recvPacket(t, relayConn, 2*time.Second) // PACKET_TYPE_REGISTRY

	cfgSeq := cfgPkt.Header.Sequence
	sendPacket(t, relayConn, NewNeonPacket(AckType, 0, 2, 1, Ack{Sequences: []uint16{cfgSeq}}), hostAddr)

	time.Sleep(600 * time.Millisecond)

	count := 0
	for {
		pkt, ok := recvPacket(t, relayConn, 50*time.Millisecond)
		if !ok {
			break
		}
		if ptype, _ := PacketTypeFromByte(pkt.Header.PacketType); ptype == SessionConfigType {
			count++
		}
	}
	if count != 0 {
		t.Errorf("received %d extra SESSION_CONFIG retransmits, want 0", count)
	}
}

func TestHostRespondsWithPong(t *testing.T) {
	host, relayConn := newTestHostWithRelay(t, nil)
	hostAddr := host.LocalAddress()

	sendPacket(t, relayConn, NewNeonPacket(PingType, 0, 2, 1, Ping{Timestamp: 12345}), hostAddr)
	pong, ok := recvPacket(t, relayConn, 2*time.Second)
	if !ok {
		t.Fatal("expected PONG from host")
	}
	if ptype, _ := PacketTypeFromByte(pong.Header.PacketType); ptype != PongType {
		t.Fatalf("packet type = %v, want PongType", ptype)
	}
	pongPayload := pong.Payload.(Pong)
	if pongPayload.OriginalTimestamp != 12345 {
		t.Errorf("OriginalTimestamp = %d, want 12345", pongPayload.OriginalTimestamp)
	}
}

func TestHostGamePacketRegistrySentOnConnect(t *testing.T) {
	host, relayConn := newTestHostWithRelay(t, nil)
	hostAddr := host.LocalAddress()

	reg := NewGamePacketRegistry()
	if err := reg.Register(0x10, "POSITION", "Player position"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	host.SetGamePacketRegistry(reg)

	sendPacket(t, relayConn, NewNeonPacket(ConnectRequestType, 1, 0, 1, ConnectRequest{ClientVersion: 1, Name: "carl", SessionID: 1, GameID: 0}), hostAddr)
	recvPacket(t, relayConn, 2*time.Second) // CONNECT_ACCEPT
	recvPacket(t, relayConn, 2*time.Second) // SESSION_CONFIG

	regPkt, ok := recvPacket(t, relayConn, 2*time.Second)
	if !ok {
		t.Fatal("expected PACKET_TYPE_REGISTRY from host")
	}
	if ptype, _ := PacketTypeFromByte(regPkt.Header.PacketType); ptype != PacketTypeRegistryType {
		t.Fatalf("packet type = %v, want PacketTypeRegistryType", ptype)
	}
	regPayload := regPkt.Payload.(PacketTypeRegistry)
	if len(regPayload.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(regPayload.Entries))
	}
	if regPayload.Entries[0].Name != "POSITION" {
		t.Errorf("Entries[0].Name = %q, want %q", regPayload.Entries[0].Name, "POSITION")
	}
}
