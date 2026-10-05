package qtineon

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// testStack spins up a relay + host + one connected client over loopback, tearing everything down after the test.
type testStack struct {
	relay  *NeonRelay
	host   *NeonHost
	client *NeonClient
	cfg    *NeonConfig
	port   int
}

func newIntegrationConfig(port int) *NeonConfig {
	cfg := DefaultConfig()
	cfg.RelayPort = port
	cfg.RelayMainLoopSleepMs = 1
	cfg.HostProcessingLoopSleepMs = 5
	cfg.ClientProcessingLoopSleepMs = 5
	cfg.HostAckTimeoutMs = 500
	cfg.ClientConnectionTimeoutMs = 2000
	cfg.ClientPingIntervalMs = 30000
	return cfg
}

func newTestStack(t *testing.T) *testStack {
	t.Helper()
	port := freeUDPPort(t)
	cfg := newIntegrationConfig(port)

	relay, err := NewRelay("127.0.0.1", cfg)
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	go func() { _ = relay.StartAndRun() }()
	time.Sleep(50 * time.Millisecond)

	host, err := NewHost(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), cfg)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	go func() { _ = host.StartAndRun() }()
	time.Sleep(100 * time.Millisecond)

	client, err := NewClient("player1", cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if ok := client.Connect(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); !ok {
		t.Fatal("client.Connect() failed")
	}
	go client.Run()
	time.Sleep(100 * time.Millisecond)

	s := &testStack{relay: relay, host: host, client: client, cfg: cfg, port: port}
	t.Cleanup(func() {
		if s.client.IsRunning() {
			_ = s.client.Stop()
		}
		if s.host.IsRunning() {
			_ = s.host.Stop()
		}
		if s.relay.IsRunning() {
			_ = s.relay.Stop()
		}
	})
	return s
}

func TestIntegrationClientAssignedID(t *testing.T) {
	s := newTestStack(t)
	cid, ok := s.client.ClientID()
	if !ok || cid != 2 {
		t.Errorf("ClientID() = (%d, %v), want (2, true)", cid, ok)
	}
}

func TestIntegrationHostSeesClient(t *testing.T) {
	s := newTestStack(t)
	clients := s.host.ConnectedClients()
	if clients[2] != "player1" {
		t.Errorf("ConnectedClients()[2] = %q, want %q", clients[2], "player1")
	}
}

func TestIntegrationSessionConfigDelivered(t *testing.T) {
	port := freeUDPPort(t)
	cfg := newIntegrationConfig(port)
	cfg.HostSessionTickRate = 30

	relay, err := NewRelay("127.0.0.1", cfg)
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	go func() { _ = relay.StartAndRun() }()
	time.Sleep(50 * time.Millisecond)
	t.Cleanup(func() {
		if relay.IsRunning() {
			_ = relay.Stop()
		}
	})

	host, err := NewHost(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), cfg)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	go func() { _ = host.StartAndRun() }()
	time.Sleep(100 * time.Millisecond)
	t.Cleanup(func() {
		if host.IsRunning() {
			_ = host.Stop()
		}
	})

	var configs []SessionConfig
	client, err := NewClient("p", cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.SetSessionConfigCallback(func(sc SessionConfig) { configs = append(configs, sc) })
	if ok := client.Connect(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); !ok {
		t.Fatal("client.Connect() failed")
	}
	go client.Run()
	t.Cleanup(func() {
		if client.IsRunning() {
			_ = client.Stop()
		}
	})
	time.Sleep(200 * time.Millisecond)

	if len(configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(configs))
	}
	if configs[0].TickRate != 30 {
		t.Errorf("TickRate = %d, want 30", configs[0].TickRate)
	}
}

func TestIntegrationRegistryDelivered(t *testing.T) {
	port := freeUDPPort(t)
	cfg := newIntegrationConfig(port)

	relay, err := NewRelay("127.0.0.1", cfg)
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	go func() { _ = relay.StartAndRun() }()
	time.Sleep(50 * time.Millisecond)
	t.Cleanup(func() {
		if relay.IsRunning() {
			_ = relay.Stop()
		}
	})

	reg := NewGamePacketRegistry()
	if err := reg.Register(0x10, "MOVE", "Player movement"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	host, err := NewHost(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), cfg)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	host.SetGamePacketRegistry(reg)
	go func() { _ = host.StartAndRun() }()
	time.Sleep(100 * time.Millisecond)
	t.Cleanup(func() {
		if host.IsRunning() {
			_ = host.Stop()
		}
	})

	var registries []PacketTypeRegistry
	client, err := NewClient("p", cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.SetPacketTypeRegistryCallback(func(r PacketTypeRegistry) { registries = append(registries, r) })
	if ok := client.Connect(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); !ok {
		t.Fatal("client.Connect() failed")
	}
	go client.Run()
	t.Cleanup(func() {
		if client.IsRunning() {
			_ = client.Stop()
		}
	})
	time.Sleep(200 * time.Millisecond)

	if len(registries) != 1 {
		t.Fatalf("len(registries) = %d, want 1", len(registries))
	}
	if len(registries[0].Entries) != 1 || registries[0].Entries[0].Name != "MOVE" {
		t.Errorf("registries[0].Entries = %v, want single MOVE entry", registries[0].Entries)
	}
}

func TestIntegrationClientToHostGamePacket(t *testing.T) {
	s := newTestStack(t)

	type received struct {
		packetType uint8
		sender     uint8
	}
	var got []received
	s.host.SetUnhandledPacketCallback(func(packetType, sender uint8, payload []byte) {
		got = append(got, received{packetType, sender})
	})

	if err := s.client.SendPacket([]byte{0xDE, 0xAD}, 0x10, 1); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if len(got) < 1 {
		t.Fatal("expected at least 1 unhandled packet on host")
	}
	if got[0].packetType != 0x10 {
		t.Errorf("packetType = %#x, want 0x10", got[0].packetType)
	}
	if got[0].sender != 2 {
		t.Errorf("sender = %d, want 2", got[0].sender)
	}
}

func TestIntegrationBroadcastToTwoClients(t *testing.T) {
	s := newTestStack(t)

	type received struct {
		packetType uint8
		sender     uint8
	}
	var got []received
	client2, err := NewClient("player2", s.cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client2.SetUnhandledPacketCallback(func(packetType, sender uint8, payload []byte) {
		got = append(got, received{packetType, sender})
	})
	if ok := client2.Connect(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(s.cfg.RelayPort))); !ok {
		t.Fatal("client2.Connect() failed")
	}
	go client2.Run()
	t.Cleanup(func() {
		if client2.IsRunning() {
			_ = client2.Stop()
		}
	})
	time.Sleep(100 * time.Millisecond)

	if err := s.client.SendPacket([]byte{0x01, 0x02}, 0x11, 0); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if len(got) < 1 {
		t.Fatal("expected client2 to receive at least 1 broadcast packet")
	}
	if got[0].packetType != 0x11 {
		t.Errorf("packetType = %#x, want 0x11", got[0].packetType)
	}
}

func TestIntegrationReconnectAfterStop(t *testing.T) {
	s := newTestStack(t)

	cid, ok := s.client.ClientID()
	if !ok || cid != 2 {
		t.Fatalf("ClientID() = (%d, %v), want (2, true)", cid, ok)
	}
	if err := s.client.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if !s.client.Reconnect(3) {
		t.Fatal("Reconnect() = false, want true")
	}
	cid, ok = s.client.ClientID()
	if !ok || cid != 2 {
		t.Errorf("ClientID() after reconnect = (%d, %v), want (2, true)", cid, ok)
	}
}

func TestIntegrationHostNotifiedOnClientDisconnect(t *testing.T) {
	s := newTestStack(t)

	var disconnected []uint8
	s.host.SetClientDisconnectCallback(func(clientID uint8) {
		disconnected = append(disconnected, clientID)
	})

	if err := s.client.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	found := false
	for _, cid := range disconnected {
		if cid == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("disconnected = %v, want to contain 2", disconnected)
	}
}

func TestIntegrationDisconnectCallbackOnClient(t *testing.T) {
	s := newTestStack(t)

	var dcEvents []uint8
	client2, err := NewClient("player2", s.cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client2.SetDisconnectCallback(func(clientID uint8) {
		dcEvents = append(dcEvents, clientID)
	})
	if ok := client2.Connect(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(s.cfg.RelayPort))); !ok {
		t.Fatal("client2.Connect() failed")
	}
	go client2.Run()
	t.Cleanup(func() {
		if client2.IsRunning() {
			_ = client2.Stop()
		}
	})
	time.Sleep(100 * time.Millisecond)

	if err := s.client.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	if len(dcEvents) < 1 {
		t.Error("expected at least 1 disconnect event on client2")
	}
}

func TestIntegrationSixClientsConnect(t *testing.T) {
	s := newTestStack(t)
	clients := []*NeonClient{s.client}

	for i := 0; i < 5; i++ {
		c, err := NewClient("player"+strconv.Itoa(i+2), s.cfg)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if ok := c.Connect(1, net.JoinHostPort("127.0.0.1", strconv.Itoa(s.cfg.RelayPort))); !ok {
			t.Fatalf("client %d failed to connect", i+2)
		}
		go c.Run()
		clients = append(clients, c)
	}
	t.Cleanup(func() {
		for _, c := range clients[1:] {
			if c.IsRunning() {
				_ = c.Stop()
			}
		}
	})

	time.Sleep(200 * time.Millisecond)
	if got := len(s.host.ConnectedClients()); got != 6 {
		t.Errorf("len(ConnectedClients()) = %d, want 6", got)
	}
}
