package qtineon

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const dtlsTestSessionID int32 = 200

// generateSelfSignedCert writes a throwaway self-signed ECDSA cert/key pair (PEM) to t.TempDir(), for use as the relay's DTLS server identity.
func generateSelfSignedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "neon-test-relay"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "relay.pem")
	keyFile = filepath.Join(dir, "relay-key.pem")

	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "EC PRIVATE KEY", keyDER)
	return certFile, keyFile
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("createFile: %v", err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatalf("pem.Encode: %v", err)
	}
}

// dtlsTestStack starts a DTLS-enabled relay and host over loopback; the relay owns the certificate, host/clients trust it via InsecureTrustAll.
type dtlsTestStack struct {
	relay      *NeonRelay
	host       *NeonHost
	relayPort  int
	clientDTLS *DtlsConfig
}

func newDtlsTestStack(t *testing.T) *dtlsTestStack {
	t.Helper()
	certFile, keyFile := generateSelfSignedCert(t)
	relayDTLS, err := FromKeyStore(certFile, keyFile)
	if err != nil {
		t.Fatalf("FromKeyStore: %v", err)
	}
	clientDTLS := InsecureTrustAll()

	relayCfg := DefaultConfig()
	relayCfg.RelayPort = freeUDPPort(t)
	relayCfg.RelayMainLoopSleepMs = 1
	relayCfg.DTLS = relayDTLS

	relay, err := NewRelay("127.0.0.1", relayCfg)
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	go func() { _ = relay.StartAndRun() }()
	waitUntil(t, 2*time.Second, relay.IsRunning)

	hostCfg := DefaultConfig()
	hostCfg.ClientConnectionTimeoutMs = 5000
	hostCfg.HostProcessingLoopSleepMs = 1
	hostCfg.DTLS = clientDTLS

	host, err := NewHost(dtlsTestSessionID, net.JoinHostPort("127.0.0.1", strconv.Itoa(relayCfg.RelayPort)), hostCfg)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	go func() { _ = host.StartAndRun() }()
	waitUntil(t, 6*time.Second, host.IsRunning)

	s := &dtlsTestStack{relay: relay, host: host, relayPort: relayCfg.RelayPort, clientDTLS: clientDTLS}
	t.Cleanup(func() {
		if host.IsRunning() {
			_ = host.Stop()
		}
		if relay.IsRunning() {
			_ = relay.Stop()
		}
	})
	return s
}

func (s *dtlsTestStack) newClientConfig() *NeonConfig {
	cfg := DefaultConfig()
	cfg.ClientConnectionTimeoutMs = 5000
	cfg.ClientProcessingLoopSleepMs = 1
	cfg.ClientPingIntervalMs = 1 << 30
	cfg.DTLS = s.clientDTLS
	return cfg
}

func (s *dtlsTestStack) connect(t *testing.T, name string) *NeonClient {
	t.Helper()
	client, err := NewClient(name, s.newClientConfig())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if ok := client.Connect(dtlsTestSessionID, net.JoinHostPort("127.0.0.1", strconv.Itoa(s.relayPort))); !ok {
		t.Fatalf("connect() returned false for %s", name)
	}
	return client
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestDtlsConnectClientIsAssignedID(t *testing.T) {
	s := newDtlsTestStack(t)
	client := s.connect(t, "dtls-player1")
	t.Cleanup(func() {
		if client.IsRunning() {
			_ = client.Stop()
		}
	})

	cid, ok := client.ClientID()
	if !ok {
		t.Fatal("ClientID() missing")
	}
	if cid < 2 {
		t.Errorf("ClientID() = %d, want >= 2", cid)
	}
}

func TestDtlsConnectClientReceivesSessionConfig(t *testing.T) {
	s := newDtlsTestStack(t)

	var configs []SessionConfig
	client, err := NewClient("dtls-player2", s.newClientConfig())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.SetSessionConfigCallback(func(sc SessionConfig) { configs = append(configs, sc) })
	if ok := client.Connect(dtlsTestSessionID, net.JoinHostPort("127.0.0.1", strconv.Itoa(s.relayPort))); !ok {
		t.Fatal("connect() returned false")
	}
	go client.Run()
	t.Cleanup(func() {
		if client.IsRunning() {
			_ = client.Stop()
		}
	})

	waitUntil(t, 5*time.Second, func() bool { return len(configs) > 0 })
	if configs[0].TickRate != 60 {
		t.Errorf("TickRate = %d, want 60", configs[0].TickRate)
	}
}

func TestDtlsGamePacketFromClientReachesHost(t *testing.T) {
	s := newDtlsTestStack(t)

	hostGotPacket := make(chan struct{}, 1)
	s.host.SetUnhandledPacketCallback(func(packetType, sender uint8, payload []byte) {
		select {
		case hostGotPacket <- struct{}{}:
		default:
		}
	})

	client := s.connect(t, "dtls-player3")
	go client.Run()
	t.Cleanup(func() {
		if client.IsRunning() {
			_ = client.Stop()
		}
	})

	if err := client.SendPacket([]byte{1, 2, 3}, 0x10, 1); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}

	select {
	case <-hostGotPacket:
	case <-time.After(5 * time.Second):
		t.Fatal("host did not receive game packet over DTLS")
	}
}

func TestDtlsReconnectAfterDisconnectSucceeds(t *testing.T) {
	s := newDtlsTestStack(t)

	cfg := s.newClientConfig()
	cfg.ClientInitialReconnectDelayMs = 100
	client, err := NewClient("dtls-reconnect", cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if ok := client.Connect(dtlsTestSessionID, net.JoinHostPort("127.0.0.1", strconv.Itoa(s.relayPort))); !ok {
		t.Fatal("connect() returned false")
	}
	t.Cleanup(func() {
		if client.IsRunning() {
			_ = client.Stop()
		}
	})

	if err := client.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	resultCh := make(chan bool, 1)
	go func() { resultCh <- client.Reconnect(1) }()

	select {
	case ok := <-resultCh:
		if !ok {
			t.Fatal("Reconnect over DTLS failed")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Reconnect over DTLS timed out")
	}
}
