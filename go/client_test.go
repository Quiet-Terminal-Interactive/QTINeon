package qtineon

import (
	"net"
	"strconv"
	"testing"
	"time"
)

func newTestClient(configure func(*NeonConfig)) (*NeonClient, error) {
	cfg := DefaultConfig()
	cfg.ClientConnectionTimeoutMs = 1000
	cfg.ClientProcessingLoopSleepMs = 5
	cfg.ClientPingIntervalMs = 30000
	if configure != nil {
		configure(cfg)
	}
	return NewClient("player1", cfg)
}

// acceptConnectInBackground waits for a CONNECT_REQUEST on relayConn and replies with CONNECT_ACCEPT; the returned channel is closed once sent.
func acceptConnectInBackground(t *testing.T, relayConn *net.UDPConn, clientID uint8, sessionID int32, token uint64) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			pkt, addr, ok := recvPacketAddr(t, relayConn, 2*time.Second)
			if !ok {
				return
			}
			if ptype, _ := PacketTypeFromByte(pkt.Header.PacketType); ptype == ConnectRequestType {
				accept := NewNeonPacket(ConnectAcceptType, 0, 1, 0, ConnectAccept{ClientID: clientID, SessionID: sessionID, Token: token})
				sendPacket(t, relayConn, accept, addr)
				return
			}
		}
	}()
	return done
}

func recvPacketAddr(t *testing.T, conn *net.UDPConn, timeout time.Duration) (NeonPacket, *net.UDPAddr, bool) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 65535)
	n, addr, err := conn.ReadFromUDP(buf)
	if err != nil {
		return NeonPacket{}, nil, false
	}
	packet, err := NeonPacketFromBytes(buf[:n])
	if err != nil {
		t.Fatalf("NeonPacketFromBytes: %v", err)
	}
	return packet, addr, true
}

func TestClientConnectSuccess(t *testing.T) {
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	done := acceptConnectInBackground(t, relayConn, 2, 1, 0x1234)
	client, err := newTestClient(nil)
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	ok := client.Connect(1, relayAddr.String())
	<-done
	if !ok {
		t.Fatal("Connect() = false, want true")
	}
	cid, has := client.ClientID()
	if !has || cid != 2 {
		t.Errorf("ClientID() = (%d, %v), want (2, true)", cid, has)
	}
	_ = client.Stop()
}

func TestClientConnectDeny(t *testing.T) {
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	go func() {
		pkt, addr, ok := recvPacketAddr(t, relayConn, 2*time.Second)
		if !ok || func() bool { pt, _ := PacketTypeFromByte(pkt.Header.PacketType); return pt != ConnectRequestType }() {
			return
		}
		deny := NewNeonPacket(ConnectDenyType, 0, 0, 0, ConnectDeny{Reason: "Full"})
		sendPacket(t, relayConn, deny, addr)
	}()

	client, err := newTestClient(nil)
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	if client.Connect(1, relayAddr.String()) {
		t.Error("Connect() = true, want false")
	}
}

func TestClientConnectTimeout(t *testing.T) {
	port := freeUDPPort(t)
	client, err := newTestClient(func(cfg *NeonConfig) {
		cfg.ClientConnectionTimeoutMs = 200
	})
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	if client.Connect(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))) {
		t.Error("Connect() = true, want false (nothing listening)")
	}
}

func TestClientConnectRequestFields(t *testing.T) {
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	type captured struct {
		req ConnectRequest
		ok  bool
	}
	resultCh := make(chan captured, 1)
	go func() {
		pkt, addr, ok := recvPacketAddr(t, relayConn, 2*time.Second)
		if !ok {
			resultCh <- captured{}
			return
		}
		req, isReq := pkt.Payload.(ConnectRequest)
		if isReq {
			accept := NewNeonPacket(ConnectAcceptType, 0, 1, 0, ConnectAccept{ClientID: 2, SessionID: 42, Token: 0})
			sendPacket(t, relayConn, accept, addr)
		}
		resultCh <- captured{req: req, ok: isReq}
	}()

	client, err := NewClient("alice", func() *NeonConfig {
		cfg := DefaultConfig()
		cfg.ClientConnectionTimeoutMs = 1000
		cfg.ClientProcessingLoopSleepMs = 5
		cfg.ClientPingIntervalMs = 30000
		return cfg
	}())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ok := client.Connect(42, relayAddr.String())
	res := <-resultCh
	if !ok {
		t.Fatal("Connect() = false, want true")
	}
	if !res.ok {
		t.Fatal("expected CONNECT_REQUEST payload")
	}
	if res.req.Name != "alice" {
		t.Errorf("Name = %q, want %q", res.req.Name, "alice")
	}
	if res.req.SessionID != 42 {
		t.Errorf("SessionID = %d, want 42", res.req.SessionID)
	}
	if res.req.ClientVersion != VERSION {
		t.Errorf("ClientVersion = %d, want %d", res.req.ClientVersion, VERSION)
	}
	_ = client.Stop()
}

func TestClientSessionConfigTriggersAck(t *testing.T) {
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	var configs []SessionConfig
	client, err := newTestClient(nil)
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	client.SetSessionConfigCallback(func(sc SessionConfig) { configs = append(configs, sc) })

	go func() {
		_, addr, ok := recvPacketAddr(t, relayConn, 2*time.Second)
		if !ok {
			return
		}
		accept := NewNeonPacket(ConnectAcceptType, 0, 1, 0, ConnectAccept{ClientID: 2, SessionID: 1, Token: 0})
		sendPacket(t, relayConn, accept, addr)
		time.Sleep(50 * time.Millisecond)
		cfgPkt := NewNeonPacket(SessionConfigType, 5, 1, 2, SessionConfig{Version: 1, TickRate: 60, MaxPacketSize: 1200})
		sendPacket(t, relayConn, cfgPkt, addr)
	}()

	if ok := client.Connect(1, relayAddr.String()); !ok {
		t.Fatal("Connect() = false, want true")
	}
	go client.Run()
	t.Cleanup(func() { _ = client.Stop() })

	ack, ok := recvPacket(t, relayConn, 2*time.Second)
	if !ok {
		t.Fatal("expected ACK from client")
	}
	if ptype, _ := PacketTypeFromByte(ack.Header.PacketType); ptype != AckType {
		t.Fatalf("packet type = %v, want AckType", ptype)
	}
	ackPayload := ack.Payload.(Ack)
	found := false
	for _, seq := range ackPayload.Sequences {
		if seq == 5 {
			found = true
		}
	}
	if !found {
		t.Errorf("Sequences = %v, want to contain 5", ackPayload.Sequences)
	}

	time.Sleep(30 * time.Millisecond)
	if len(configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(configs))
	}
	if configs[0].TickRate != 60 {
		t.Errorf("TickRate = %d, want 60", configs[0].TickRate)
	}
}

func TestClientRespondsToPingWithPong(t *testing.T) {
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	client, err := newTestClient(nil)
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}

	go func() {
		_, addr, ok := recvPacketAddr(t, relayConn, 2*time.Second)
		if !ok {
			return
		}
		accept := NewNeonPacket(ConnectAcceptType, 0, 1, 0, ConnectAccept{ClientID: 2, SessionID: 1, Token: 0})
		sendPacket(t, relayConn, accept, addr)
		time.Sleep(50 * time.Millisecond)
		ping := NewNeonPacket(PingType, 0, 1, 2, Ping{Timestamp: 99999})
		sendPacket(t, relayConn, ping, addr)
	}()

	if ok := client.Connect(1, relayAddr.String()); !ok {
		t.Fatal("Connect() = false, want true")
	}
	go client.Run()
	t.Cleanup(func() { _ = client.Stop() })

	pong, ok := recvPacket(t, relayConn, 2*time.Second)
	if !ok {
		t.Fatal("expected PONG from client")
	}
	if ptype, _ := PacketTypeFromByte(pong.Header.PacketType); ptype != PongType {
		t.Fatalf("packet type = %v, want PongType", ptype)
	}
	pongPayload := pong.Payload.(Pong)
	if pongPayload.OriginalTimestamp != 99999 {
		t.Errorf("OriginalTimestamp = %d, want 99999", pongPayload.OriginalTimestamp)
	}
}

func TestClientAutoPingSent(t *testing.T) {
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	client, err := newTestClient(func(cfg *NeonConfig) {
		cfg.ClientPingIntervalMs = 50
	})
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}

	done := acceptConnectInBackground(t, relayConn, 2, 1, 0)
	if ok := client.Connect(1, relayAddr.String()); !ok {
		t.Fatal("Connect() = false, want true")
	}
	<-done
	go client.Run()
	t.Cleanup(func() { _ = client.Stop() })

	for {
		pkt, ok := recvPacket(t, relayConn, 2*time.Second)
		if !ok {
			t.Fatal("expected PING from client")
		}
		if ptype, _ := PacketTypeFromByte(pkt.Header.PacketType); ptype == PingType {
			break
		}
	}
}

func TestClientStopSendsDisconnectNotice(t *testing.T) {
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	done := acceptConnectInBackground(t, relayConn, 2, 1, 0)
	client, err := newTestClient(nil)
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	if ok := client.Connect(1, relayAddr.String()); !ok {
		t.Fatal("Connect() = false, want true")
	}
	<-done
	_ = client.Stop()

	for {
		pkt, ok := recvPacket(t, relayConn, 2*time.Second)
		if !ok {
			t.Fatal("expected DISCONNECT_NOTICE from client")
		}
		if ptype, _ := PacketTypeFromByte(pkt.Header.PacketType); ptype == DisconnectNoticeType {
			break
		}
	}
}

func TestClientSendPacketGameType(t *testing.T) {
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	done := acceptConnectInBackground(t, relayConn, 2, 1, 0)
	client, err := newTestClient(nil)
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	if ok := client.Connect(1, relayAddr.String()); !ok {
		t.Fatal("Connect() = false, want true")
	}
	<-done

	if err := client.SendPacket([]byte{1, 2, 3}, 0x10, 0); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	for {
		pkt, ok := recvPacket(t, relayConn, 2*time.Second)
		if !ok {
			t.Fatal("expected GAME_PACKET from client")
		}
		ptype, _ := PacketTypeFromByte(pkt.Header.PacketType)
		if ptype != GamePacketType {
			continue
		}
		gp := pkt.Payload.(GamePacket)
		if string(gp.RawPayload) != "\x01\x02\x03" {
			t.Errorf("RawPayload = %v, want [1 2 3]", gp.RawPayload)
		}
		break
	}
}

func TestClientUnhandledCallbackForGamePackets(t *testing.T) {
	relayConn := newRawTestConn(t)
	relayAddr := relayConn.LocalAddr().(*net.UDPAddr)

	type received struct {
		packetType uint8
		sender     uint8
	}
	var got []received
	client, err := newTestClient(nil)
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	client.SetUnhandledPacketCallback(func(packetType, sender uint8, payload []byte) {
		got = append(got, received{packetType, sender})
	})

	go func() {
		_, addr, ok := recvPacketAddr(t, relayConn, 2*time.Second)
		if !ok {
			return
		}
		accept := NewNeonPacket(ConnectAcceptType, 0, 1, 0, ConnectAccept{ClientID: 2, SessionID: 1, Token: 0})
		sendPacket(t, relayConn, accept, addr)
		time.Sleep(50 * time.Millisecond)
		gp := NewNeonPacket(GamePacketType, 0, 1, 2, GamePacket{RawPayload: []byte("game_data")})
		sendPacket(t, relayConn, gp, addr)
	}()

	if ok := client.Connect(1, relayAddr.String()); !ok {
		t.Fatal("Connect() = false, want true")
	}
	go client.Run()
	t.Cleanup(func() { _ = client.Stop() })

	deadline := time.Now().Add(2 * time.Second)
	for len(got) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) < 1 {
		t.Fatal("expected at least 1 unhandled packet callback")
	}
	if got[0].packetType != 0x10 {
		t.Errorf("packetType = %#x, want 0x10", got[0].packetType)
	}
}
