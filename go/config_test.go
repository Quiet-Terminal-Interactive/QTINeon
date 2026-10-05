package qtineon

import "testing"

func TestDefaultConfigValues(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.RelayPort != 7777 {
		t.Errorf("RelayPort = %d, want 7777", cfg.RelayPort)
	}
	if cfg.MaxPacketsPerSecond != 100 {
		t.Errorf("MaxPacketsPerSecond = %d, want 100", cfg.MaxPacketsPerSecond)
	}
	if cfg.HostSessionTickRate != 60 {
		t.Errorf("HostSessionTickRate = %d, want 60", cfg.HostSessionTickRate)
	}
	if cfg.ClientMaxReconnectAttempts != 6 {
		t.Errorf("ClientMaxReconnectAttempts = %d, want 6", cfg.ClientMaxReconnectAttempts)
	}
	if cfg.DTLS != nil {
		t.Error("DTLS should be nil by default")
	}
	if cfg.IsDTLSEnabled() {
		t.Error("IsDTLSEnabled() should be false by default")
	}
}

func TestConfigCustomValues(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RelayPort = 9999
	cfg.HostSessionTickRate = 30
	if cfg.RelayPort != 9999 {
		t.Errorf("RelayPort = %d, want 9999", cfg.RelayPort)
	}
	if cfg.HostSessionTickRate != 30 {
		t.Errorf("HostSessionTickRate = %d, want 30", cfg.HostSessionTickRate)
	}
}

func TestConfigValidateBufferSizeMinimum(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BufferSize = 7
	if err := cfg.validate(); err == nil {
		t.Error("expected error for BufferSize below minimum")
	}
}

func TestConfigValidatePoolMaxLessThanInit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BufferPoolInitSize = 10
	cfg.BufferPoolMaxSize = 5
	if err := cfg.validate(); err == nil {
		t.Error("expected error when BufferPoolMaxSize < BufferPoolInitSize")
	}
}

func TestConfigValidateRelayPortZeroAllowed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RelayPort = 0
	if err := cfg.validate(); err != nil {
		t.Errorf("unexpected error for RelayPort=0: %v", err)
	}
}

func TestConfigValidateNegativeCleanupInterval(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RelayCleanupIntervalMs = -1
	if err := cfg.validate(); err == nil {
		t.Error("expected error for negative RelayCleanupIntervalMs")
	}
}

func TestConfigValidateMaxPendingConnectionsZero(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPendingConnections = 0
	if err := cfg.validate(); err == nil {
		t.Error("expected error for MaxPendingConnections=0")
	}
}

func TestConfigValidateTickRateZero(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HostSessionTickRate = 0
	if err := cfg.validate(); err == nil {
		t.Error("expected error for HostSessionTickRate=0")
	}
}

func TestConfigValidateMaxPacketSizeZero(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HostSessionMaxPacketSize = 0
	if err := cfg.validate(); err == nil {
		t.Error("expected error for HostSessionMaxPacketSize=0")
	}
}

func TestConfigValidateNegativeAckTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HostAckTimeoutMs = -1
	if err := cfg.validate(); err == nil {
		t.Error("expected error for negative HostAckTimeoutMs")
	}
}

func TestConfigValidateNegativeConnectionTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ClientConnectionTimeoutMs = -1
	if err := cfg.validate(); err == nil {
		t.Error("expected error for negative ClientConnectionTimeoutMs")
	}
}

func TestConfigValidateNegativeReliableTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ReliablePacketTimeoutMs = -1
	if err := cfg.validate(); err == nil {
		t.Error("expected error for negative ReliablePacketTimeoutMs")
	}
}

func TestConfigValidateZeroValuesAllowedWhereAppropriate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RelayCleanupIntervalMs = 0
	cfg.HostAckTimeoutMs = 0
	cfg.ClientConnectionTimeoutMs = 0
	if err := cfg.validate(); err != nil {
		t.Errorf("unexpected error for zero timeouts: %v", err)
	}
}

func TestConfigDtlsEnabledFlag(t *testing.T) {
	dc := InsecureTrustAll()
	cfg := DefaultConfig()
	cfg.DTLS = dc
	if !cfg.IsDTLSEnabled() {
		t.Error("IsDTLSEnabled() should be true when DTLS is set")
	}
	if cfg.DTLS != dc {
		t.Error("DTLS should be the same pointer that was set")
	}
}
