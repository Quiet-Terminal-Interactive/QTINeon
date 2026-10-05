package qtineon

import (
	"testing"
)

func TestPacketTypeFromByteKnown(t *testing.T) {
	cases := []struct {
		b    byte
		want PacketType
	}{
		{0x01, ConnectRequestType},
		{0x06, HostRegisterType},
		{0x0E, AckType},
	}
	for _, c := range cases {
		got, err := PacketTypeFromByte(c.b)
		if err != nil {
			t.Fatalf("PacketTypeFromByte(0x%02x): unexpected error: %v", c.b, err)
		}
		if got != c.want {
			t.Errorf("PacketTypeFromByte(0x%02x) = %v, want %v", c.b, got, c.want)
		}
	}
}

func TestPacketTypeFromByteGamePacket(t *testing.T) {
	for b := 0x10; b < 0x20; b++ {
		got, err := PacketTypeFromByte(byte(b))
		if err != nil {
			t.Fatalf("PacketTypeFromByte(0x%02x): unexpected error: %v", b, err)
		}
		if got != GamePacketType {
			t.Errorf("PacketTypeFromByte(0x%02x) = %v, want GamePacketType", b, got)
		}
	}
}

func TestPacketTypeFromByteUnknown(t *testing.T) {
	if _, err := PacketTypeFromByte(0x07); err == nil {
		t.Error("expected error for unknown packet type 0x07")
	}
}

func TestPacketTypeIsCorePacket(t *testing.T) {
	if !ConnectRequestType.IsCorePacket() {
		t.Error("ConnectRequestType should be a core packet")
	}
	if GamePacketType.IsCorePacket() {
		t.Error("GamePacketType should not be a core packet")
	}
}

func TestPacketHeaderRoundTrip(t *testing.T) {
	h := NewPacketHeader(0x01, 42, 3, 1)
	data, err := h.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if len(data) != HeaderSize {
		t.Fatalf("len(data) = %d, want %d", len(data), HeaderSize)
	}
	h2, err := UnmarshalPacketHeader(data)
	if err != nil {
		t.Fatalf("UnmarshalPacketHeader: %v", err)
	}
	if h != h2 {
		t.Errorf("round trip mismatch: %+v != %+v", h, h2)
	}
}

func TestPacketHeaderLittleEndianMagic(t *testing.T) {
	h := NewPacketHeader(0x01, 0, 0, 0)
	raw, err := h.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if raw[0] != 0x45 || raw[1] != 0x4E {
		t.Errorf("magic bytes = [0x%02x, 0x%02x], want [0x45, 0x4E]", raw[0], raw[1])
	}
}

func TestPacketHeaderSequenceWraps(t *testing.T) {
	h := NewPacketHeader(0x01, 65535, 0, 0)
	data, _ := h.MarshalBinary()
	h2, err := UnmarshalPacketHeader(data)
	if err != nil {
		t.Fatalf("UnmarshalPacketHeader: %v", err)
	}
	if h2.Sequence != 65535 {
		t.Errorf("Sequence = %d, want 65535", h2.Sequence)
	}
}

func TestPacketHeaderTooShort(t *testing.T) {
	if _, err := UnmarshalPacketHeader([]byte{0x45, 0x4E}); err == nil {
		t.Error("expected error for too-short header")
	}
}

func TestPacketHeaderBadMagic(t *testing.T) {
	bad := []byte{0x00, 0x00, VERSION, 0x01, 0, 0, 0, 1}
	if _, err := UnmarshalPacketHeader(bad); err == nil {
		t.Error("expected error for bad magic")
	}
}

func TestConnectRequestRoundTrip(t *testing.T) {
	p := ConnectRequest{ClientVersion: 1, Name: "alice", SessionID: 42, GameID: 7}
	data, err := p.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	p2, err := UnmarshalConnectRequest(data)
	if err != nil {
		t.Fatalf("UnmarshalConnectRequest: %v", err)
	}
	if p != p2 {
		t.Errorf("round trip mismatch: %+v != %+v", p, p2)
	}
}

func TestConnectRequestNamePreserved(t *testing.T) {
	p := ConnectRequest{ClientVersion: 1, Name: "björn", SessionID: 1, GameID: 0}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalConnectRequest(data)
	if err != nil {
		t.Fatalf("UnmarshalConnectRequest: %v", err)
	}
	if p2.Name != "björn" {
		t.Errorf("Name = %q, want %q", p2.Name, "björn")
	}
}

func TestConnectRequestTooShort(t *testing.T) {
	if _, err := UnmarshalConnectRequest([]byte{0x01, 0x01}); err == nil {
		t.Error("expected error for too-short ConnectRequest")
	}
}

func TestConnectRequestNameTooLong(t *testing.T) {
	longName := make([]byte, 65)
	for i := range longName {
		longName[i] = 'x'
	}
	p := ConnectRequest{ClientVersion: 1, Name: string(longName), SessionID: 1, GameID: 0}
	data, _ := p.MarshalBinary()
	if _, err := UnmarshalConnectRequest(data); err == nil {
		t.Error("expected error for name too long")
	}
}

func TestConnectAcceptRoundTrip(t *testing.T) {
	p := ConnectAccept{ClientID: 2, SessionID: 42, Token: 0x0EADBEEFC0FFEE42}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalConnectAccept(data)
	if err != nil {
		t.Fatalf("UnmarshalConnectAccept: %v", err)
	}
	if p != p2 {
		t.Errorf("round trip mismatch: %+v != %+v", p, p2)
	}
}

func TestConnectAcceptSize(t *testing.T) {
	p := ConnectAccept{ClientID: 1, SessionID: 1, Token: 0}
	data, _ := p.MarshalBinary()
	if len(data) != 13 {
		t.Errorf("len(data) = %d, want 13", len(data))
	}
}

func TestConnectDenyRoundTrip(t *testing.T) {
	p := ConnectDeny{Reason: "Session full"}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalConnectDeny(data)
	if err != nil {
		t.Fatalf("UnmarshalConnectDeny: %v", err)
	}
	if p != p2 {
		t.Errorf("round trip mismatch: %+v != %+v", p, p2)
	}
}

func TestSessionConfigRoundTrip(t *testing.T) {
	p := SessionConfig{Version: 1, TickRate: 60, MaxPacketSize: 1200}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalSessionConfig(data)
	if err != nil {
		t.Fatalf("UnmarshalSessionConfig: %v", err)
	}
	if p != p2 {
		t.Errorf("round trip mismatch: %+v != %+v", p, p2)
	}
}

func TestSessionConfigSize(t *testing.T) {
	p := SessionConfig{Version: 1, TickRate: 60, MaxPacketSize: 1200}
	data, _ := p.MarshalBinary()
	if len(data) != 5 {
		t.Errorf("len(data) = %d, want 5", len(data))
	}
}

func TestPacketTypeRegistryEmptyRoundTrip(t *testing.T) {
	p := PacketTypeRegistry{}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalPacketTypeRegistry(data)
	if err != nil {
		t.Fatalf("UnmarshalPacketTypeRegistry: %v", err)
	}
	if len(p2.Entries) != 0 {
		t.Errorf("len(Entries) = %d, want 0", len(p2.Entries))
	}
}

func TestPacketTypeRegistryWithEntriesRoundTrip(t *testing.T) {
	p := PacketTypeRegistry{Entries: []PacketTypeEntry{
		{PacketID: 0x10, Name: "POSITION", Description: "Player position"},
		{PacketID: 0x11, Name: "CHAT", Description: "Chat message"},
	}}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalPacketTypeRegistry(data)
	if err != nil {
		t.Fatalf("UnmarshalPacketTypeRegistry: %v", err)
	}
	if len(p2.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(p2.Entries))
	}
	if p2.Entries[0].Name != "POSITION" {
		t.Errorf("Entries[0].Name = %q, want %q", p2.Entries[0].Name, "POSITION")
	}
	if p2.Entries[1].Description != "Chat message" {
		t.Errorf("Entries[1].Description = %q, want %q", p2.Entries[1].Description, "Chat message")
	}
}

func TestHostRegisterRoundTrip(t *testing.T) {
	p := HostRegister{SessionID: 99, HostToken: 0xABCDEF0123456789}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalHostRegister(data)
	if err != nil {
		t.Fatalf("UnmarshalHostRegister: %v", err)
	}
	if p != p2 {
		t.Errorf("round trip mismatch: %+v != %+v", p, p2)
	}
}

func TestHostRegisterSize(t *testing.T) {
	p := HostRegister{SessionID: 1, HostToken: 0}
	data, _ := p.MarshalBinary()
	if len(data) != 12 {
		t.Errorf("len(data) = %d, want 12", len(data))
	}
}

func TestPingRoundTrip(t *testing.T) {
	p := Ping{Timestamp: 123456789}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalPing(data)
	if err != nil {
		t.Fatalf("UnmarshalPing: %v", err)
	}
	if p != p2 {
		t.Errorf("round trip mismatch: %+v != %+v", p, p2)
	}
}

func TestPongRoundTrip(t *testing.T) {
	p := Pong{OriginalTimestamp: 987654321}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalPong(data)
	if err != nil {
		t.Fatalf("UnmarshalPong: %v", err)
	}
	if p != p2 {
		t.Errorf("round trip mismatch: %+v != %+v", p, p2)
	}
}

func TestDisconnectNoticeRoundTrip(t *testing.T) {
	p := DisconnectNotice{}
	data, _ := p.MarshalBinary()
	if len(data) != 0 {
		t.Errorf("len(data) = %d, want 0", len(data))
	}
	p2, err := UnmarshalDisconnectNotice(data)
	if err != nil {
		t.Fatalf("UnmarshalDisconnectNotice: %v", err)
	}
	if p != p2 {
		t.Errorf("round trip mismatch: %+v != %+v", p, p2)
	}
}

func TestAckRoundTripSingle(t *testing.T) {
	p := Ack{Sequences: []uint16{42}}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalAck(data)
	if err != nil {
		t.Fatalf("UnmarshalAck: %v", err)
	}
	if len(p2.Sequences) != 1 || p2.Sequences[0] != 42 {
		t.Errorf("Sequences = %v, want [42]", p2.Sequences)
	}
}

func TestAckRoundTripMultiple(t *testing.T) {
	p := Ack{Sequences: []uint16{1, 2, 3, 100}}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalAck(data)
	if err != nil {
		t.Fatalf("UnmarshalAck: %v", err)
	}
	want := []uint16{1, 2, 3, 100}
	if len(p2.Sequences) != len(want) {
		t.Fatalf("len(Sequences) = %d, want %d", len(p2.Sequences), len(want))
	}
	for i, s := range want {
		if p2.Sequences[i] != s {
			t.Errorf("Sequences[%d] = %d, want %d", i, p2.Sequences[i], s)
		}
	}
}

func TestReconnectRequestRoundTrip(t *testing.T) {
	p := ReconnectRequest{Token: 0x1234567890ABCDEF, SessionID: 42, PreviousClientID: 3}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalReconnectRequest(data)
	if err != nil {
		t.Fatalf("UnmarshalReconnectRequest: %v", err)
	}
	if p != p2 {
		t.Errorf("round trip mismatch: %+v != %+v", p, p2)
	}
}

func TestReconnectRequestSize(t *testing.T) {
	p := ReconnectRequest{Token: 0, SessionID: 0, PreviousClientID: 0}
	data, _ := p.MarshalBinary()
	if len(data) != 13 {
		t.Errorf("len(data) = %d, want 13", len(data))
	}
}

func TestGamePacketRoundTrip(t *testing.T) {
	p := GamePacket{RawPayload: []byte{0x01, 0x02, 0x03}}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalGamePacket(data)
	if err != nil {
		t.Fatalf("UnmarshalGamePacket: %v", err)
	}
	if string(p2.RawPayload) != string(p.RawPayload) {
		t.Errorf("RawPayload = %v, want %v", p2.RawPayload, p.RawPayload)
	}
}

func TestGamePacketEmpty(t *testing.T) {
	p := GamePacket{RawPayload: []byte{}}
	data, _ := p.MarshalBinary()
	p2, err := UnmarshalGamePacket(data)
	if err != nil {
		t.Fatalf("UnmarshalGamePacket: %v", err)
	}
	if len(p2.RawPayload) != 0 {
		t.Errorf("len(RawPayload) = %d, want 0", len(p2.RawPayload))
	}
}

func TestNeonPacketRoundTripPing(t *testing.T) {
	pkt := NewNeonPacket(PingType, 7, 2, 1, Ping{Timestamp: 999})
	data, err := pkt.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
	pkt2, err := NeonPacketFromBytes(data)
	if err != nil {
		t.Fatalf("NeonPacketFromBytes: %v", err)
	}
	if pkt2.Header != pkt.Header {
		t.Errorf("header mismatch: %+v != %+v", pkt2.Header, pkt.Header)
	}
	ping, ok := pkt2.Payload.(Ping)
	if !ok {
		t.Fatalf("payload type = %T, want Ping", pkt2.Payload)
	}
	if ping.Timestamp != 999 {
		t.Errorf("Timestamp = %d, want 999", ping.Timestamp)
	}
}

func TestNeonPacketRoundTripAllTypes(t *testing.T) {
	cases := []struct {
		ptype   PacketType
		payload Payload
	}{
		{ConnectRequestType, ConnectRequest{ClientVersion: 1, Name: "bob", SessionID: 1, GameID: 0}},
		{ConnectAcceptType, ConnectAccept{ClientID: 2, SessionID: 1, Token: 12345}},
		{ConnectDenyType, ConnectDeny{Reason: "Full"}},
		{SessionConfigType, SessionConfig{Version: 1, TickRate: 60, MaxPacketSize: 1200}},
		{PacketTypeRegistryType, PacketTypeRegistry{}},
		{HostRegisterType, HostRegister{SessionID: 1, HostToken: 0xFFFFFFFFFFFFFFFF}},
		{PingType, Ping{Timestamp: 0}},
		{PongType, Pong{OriginalTimestamp: 0}},
		{DisconnectNoticeType, DisconnectNotice{}},
		{AckType, Ack{Sequences: []uint16{5}}},
		{ReconnectRequestType, ReconnectRequest{Token: 99, SessionID: 1, PreviousClientID: 3}},
		{GamePacketType, GamePacket{RawPayload: []byte("hello")}},
	}
	for _, c := range cases {
		pkt := NewNeonPacket(c.ptype, 0, 0, 0, c.payload)
		data, err := pkt.ToBytes()
		if err != nil {
			t.Fatalf("ToBytes for %v: %v", c.ptype, err)
		}
		pkt2, err := NeonPacketFromBytes(data)
		if err != nil {
			t.Fatalf("NeonPacketFromBytes for %v: %v", c.ptype, err)
		}
		got, err := PacketTypeFromByte(pkt2.Header.PacketType)
		if err != nil {
			t.Fatalf("PacketTypeFromByte for %v: %v", c.ptype, err)
		}
		if got != c.ptype {
			t.Errorf("round trip type mismatch: got %v, want %v", got, c.ptype)
		}
	}
}

func TestNeonPacketTooShort(t *testing.T) {
	if _, err := NeonPacketFromBytes([]byte{0x45, 0x4E, 0x01, 0x01}); err == nil {
		t.Error("expected error for too-short packet")
	}
}

func TestNeonPacketBadMagic(t *testing.T) {
	bad := []byte{0x00, 0x00, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00}
	if _, err := NeonPacketFromBytes(bad); err == nil {
		t.Error("expected error for bad magic")
	}
}

func TestNeonPacketTotalLength(t *testing.T) {
	pkt := NewNeonPacket(PingType, 0, 0, 0, Ping{Timestamp: 0})
	data, err := pkt.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
	if len(data) != HeaderSize+8 {
		t.Errorf("len(data) = %d, want %d", len(data), HeaderSize+8)
	}
}

func TestSigned16Positive(t *testing.T) {
	if got := signed16(10); got != 10 {
		t.Errorf("signed16(10) = %d, want 10", got)
	}
}

func TestSigned16MaxPositive(t *testing.T) {
	if got := signed16(32767); got != 32767 {
		t.Errorf("signed16(32767) = %d, want 32767", got)
	}
}

func TestSigned16WrapsToNegative(t *testing.T) {
	if got := signed16(32768); got != -32768 {
		t.Errorf("signed16(32768) = %d, want -32768", got)
	}
}

func TestSigned16MinusOne(t *testing.T) {
	if got := signed16(65535); got != -1 {
		t.Errorf("signed16(65535) = %d, want -1", got)
	}
}

func TestSigned16WrapAroundDetection(t *testing.T) {
	if got := signed16(1 - 65535); got <= 0 {
		t.Errorf("signed16(1-65535) = %d, want > 0", got)
	}
	if got := signed16(65534 - 0); got >= 0 {
		t.Errorf("signed16(65534-0) = %d, want < 0", got)
	}
	if got := signed16(0 - 1); got >= 0 {
		t.Errorf("signed16(0-1) = %d, want < 0", got)
	}
	if got := signed16(2 - 0); got <= 0 {
		t.Errorf("signed16(2-0) = %d, want > 0", got)
	}
}
