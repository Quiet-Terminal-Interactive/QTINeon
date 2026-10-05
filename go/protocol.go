package qtineon

import (
	"encoding/binary"
	"fmt"
)

// MAGIC is the wire magic bytes 0x4E 0x45 ("NE").
const MAGIC uint16 = 0x4E45

// VERSION is the current protocol version byte.
const VERSION uint8 = 0x01

// HeaderSize is the fixed byte length of every packet header.
const HeaderSize = 8

const (
	maxNameLength        = 64
	maxDescriptionLength = 256
	maxPacketCount       = 100
)

// signed16 interprets n (0-65535) as a signed 16-bit integer, matching the Python/Java sequence-wraparound comparison semantics.
func signed16(n int) int16 {
	n &= 0xFFFF
	if n >= 0x8000 {
		n -= 0x10000
	}
	return int16(n)
}

// PacketType identifies the kind of payload a packet carries.
//
// Values 0x10 and above are treated as GamePacket and are application-defined.
type PacketType uint8

const (
	ConnectRequestType     PacketType = 0x01
	ConnectAcceptType      PacketType = 0x02
	ConnectDenyType        PacketType = 0x03
	SessionConfigType      PacketType = 0x04
	PacketTypeRegistryType PacketType = 0x05
	HostRegisterType       PacketType = 0x06
	PingType               PacketType = 0x0B
	PongType               PacketType = 0x0C
	DisconnectNoticeType   PacketType = 0x0D
	AckType                PacketType = 0x0E
	ReconnectRequestType   PacketType = 0x0F
	GamePacketType         PacketType = 0x10
)

// PacketTypeFromByte returns the PacketType for wire byte b.
//
// Any value >= 0x10 returns GamePacketType.
func PacketTypeFromByte(b byte) (PacketType, error) {
	if b >= 0x10 {
		return GamePacketType, nil
	}
	switch PacketType(b) {
	case ConnectRequestType, ConnectAcceptType, ConnectDenyType, SessionConfigType,
		PacketTypeRegistryType, HostRegisterType, PingType, PongType,
		DisconnectNoticeType, AckType, ReconnectRequestType:
		return PacketType(b), nil
	default:
		return 0, fmt.Errorf("unknown packet type: 0x%02x", b)
	}
}

// IsCorePacket reports whether t is a protocol-defined type, as opposed to GamePacketType.
func (t PacketType) IsCorePacket() bool {
	return t != GamePacketType
}

// PacketHeader is the fixed 8-byte little-endian packet header.
//
// Layout: magic(2) version(1) packetType(1) sequence(2) clientID(1) destinationID(1).
type PacketHeader struct {
	Magic         uint16
	Version       uint8
	PacketType    uint8
	Sequence      uint16
	ClientID      uint8
	DestinationID uint8
}

// NewPacketHeader creates a header with the canonical magic and current version.
func NewPacketHeader(packetType uint8, sequence uint16, clientID, destinationID uint8) PacketHeader {
	return PacketHeader{
		Magic:         MAGIC,
		Version:       VERSION,
		PacketType:    packetType,
		Sequence:      sequence,
		ClientID:      clientID,
		DestinationID: destinationID,
	}
}

// MarshalBinary serialises the header to 8 bytes, little-endian.
func (h PacketHeader) MarshalBinary() ([]byte, error) {
	buf := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint16(buf[0:2], h.Magic)
	buf[2] = h.Version
	buf[3] = h.PacketType
	binary.LittleEndian.PutUint16(buf[4:6], h.Sequence)
	buf[6] = h.ClientID
	buf[7] = h.DestinationID
	return buf, nil
}

// UnmarshalPacketHeader parses a header from the first 8 bytes of data.
func UnmarshalPacketHeader(data []byte) (PacketHeader, error) {
	if len(data) < HeaderSize {
		return PacketHeader{}, fmt.Errorf("buffer too short for header: %d", len(data))
	}
	h := PacketHeader{
		Magic:         binary.LittleEndian.Uint16(data[0:2]),
		Version:       data[2],
		PacketType:    data[3],
		Sequence:      binary.LittleEndian.Uint16(data[4:6]),
		ClientID:      data[6],
		DestinationID: data[7],
	}
	if h.Magic != MAGIC {
		return PacketHeader{}, fmt.Errorf("invalid magic: 0x%04x", h.Magic)
	}
	return h, nil
}

// Payload is implemented by every packet payload type.
type Payload interface {
	MarshalBinary() ([]byte, error)
}

// ConnectRequest is the payload for ConnectRequestType.
//
// Sent by a client to request joining a session.
type ConnectRequest struct {
	ClientVersion uint8
	Name          string
	SessionID     int32
	GameID        int32
}

func (p ConnectRequest) MarshalBinary() ([]byte, error) {
	nameBytes := []byte(p.Name)
	buf := make([]byte, 3+len(nameBytes)+8)
	buf[0] = p.ClientVersion
	binary.LittleEndian.PutUint16(buf[1:3], uint16(len(nameBytes)))
	copy(buf[3:], nameBytes)
	off := 3 + len(nameBytes)
	binary.LittleEndian.PutUint32(buf[off:off+4], uint32(p.SessionID))
	binary.LittleEndian.PutUint32(buf[off+4:off+8], uint32(p.GameID))
	return buf, nil
}

func UnmarshalConnectRequest(data []byte) (ConnectRequest, error) {
	if len(data) < 3 {
		return ConnectRequest{}, fmt.Errorf("ConnectRequest too short")
	}
	version := data[0]
	nameLen := int(binary.LittleEndian.Uint16(data[1:3]))
	if nameLen < 1 || nameLen > maxNameLength {
		return ConnectRequest{}, fmt.Errorf("invalid name length: %d", nameLen)
	}
	offset := 3
	if len(data) < offset+nameLen+8 {
		return ConnectRequest{}, fmt.Errorf("ConnectRequest truncated")
	}
	name := string(data[offset : offset+nameLen])
	sessionID := int32(binary.LittleEndian.Uint32(data[offset+nameLen : offset+nameLen+4]))
	gameID := int32(binary.LittleEndian.Uint32(data[offset+nameLen+4 : offset+nameLen+8]))
	return ConnectRequest{version, name, sessionID, gameID}, nil
}

// ConnectAccept is the payload for ConnectAcceptType.
//
// Sent by the host to confirm a connection and assign a client ID and token.
// Also sent by the relay to the host on successful HOST_REGISTER (ClientID == 1).
type ConnectAccept struct {
	ClientID  uint8
	SessionID int32
	Token     uint64
}

func (p ConnectAccept) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 13)
	buf[0] = p.ClientID
	binary.LittleEndian.PutUint32(buf[1:5], uint32(p.SessionID))
	binary.LittleEndian.PutUint64(buf[5:13], p.Token)
	return buf, nil
}

func UnmarshalConnectAccept(data []byte) (ConnectAccept, error) {
	if len(data) < 13 {
		return ConnectAccept{}, fmt.Errorf("ConnectAccept too short: %d", len(data))
	}
	return ConnectAccept{
		ClientID:  data[0],
		SessionID: int32(binary.LittleEndian.Uint32(data[1:5])),
		Token:     binary.LittleEndian.Uint64(data[5:13]),
	}, nil
}

// ConnectDeny is the payload for ConnectDenyType.
//
// Sent when a connection or reconnect request is rejected.
type ConnectDeny struct {
	Reason string
}

func (p ConnectDeny) MarshalBinary() ([]byte, error) {
	reasonBytes := []byte(p.Reason)
	buf := make([]byte, 2+len(reasonBytes))
	binary.LittleEndian.PutUint16(buf[0:2], uint16(len(reasonBytes)))
	copy(buf[2:], reasonBytes)
	return buf, nil
}

func UnmarshalConnectDeny(data []byte) (ConnectDeny, error) {
	if len(data) < 2 {
		return ConnectDeny{}, fmt.Errorf("ConnectDeny too short")
	}
	reasonLen := int(binary.LittleEndian.Uint16(data[0:2]))
	if reasonLen < 1 || reasonLen > maxDescriptionLength {
		return ConnectDeny{}, fmt.Errorf("invalid reason length: %d", reasonLen)
	}
	if len(data) < 2+reasonLen {
		return ConnectDeny{}, fmt.Errorf("ConnectDeny truncated")
	}
	return ConnectDeny{Reason: string(data[2 : 2+reasonLen])}, nil
}

// SessionConfig is the payload for SessionConfigType.
//
// Sent reliably by the host to each connecting client.
type SessionConfig struct {
	Version       uint8
	TickRate      int16
	MaxPacketSize int16
}

func (p SessionConfig) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 5)
	buf[0] = p.Version
	binary.LittleEndian.PutUint16(buf[1:3], uint16(p.TickRate))
	binary.LittleEndian.PutUint16(buf[3:5], uint16(p.MaxPacketSize))
	return buf, nil
}

func UnmarshalSessionConfig(data []byte) (SessionConfig, error) {
	if len(data) < 5 {
		return SessionConfig{}, fmt.Errorf("SessionConfig too short: %d", len(data))
	}
	return SessionConfig{
		Version:       data[0],
		TickRate:      int16(binary.LittleEndian.Uint16(data[1:3])),
		MaxPacketSize: int16(binary.LittleEndian.Uint16(data[3:5])),
	}, nil
}

// PacketTypeEntry is a single entry in a PacketTypeRegistry.
type PacketTypeEntry struct {
	PacketID    uint8
	Name        string
	Description string
}

func (e PacketTypeEntry) MarshalBinary() ([]byte, error) {
	nameBytes := []byte(e.Name)
	descBytes := []byte(e.Description)
	buf := make([]byte, 0, 3+len(nameBytes)+len(descBytes))
	buf = append(buf, e.PacketID, byte(len(nameBytes)))
	buf = append(buf, nameBytes...)
	buf = append(buf, byte(len(descBytes)))
	buf = append(buf, descBytes...)
	return buf, nil
}

func unmarshalPacketTypeEntry(data []byte, offset int) (PacketTypeEntry, int, error) {
	if len(data)-offset < 3 {
		return PacketTypeEntry{}, 0, fmt.Errorf("PacketTypeEntry too short")
	}
	packetID := data[offset]
	nameLen := int(data[offset+1])
	if nameLen > maxNameLength {
		return PacketTypeEntry{}, 0, fmt.Errorf("PacketTypeEntry name too long: %d", nameLen)
	}
	offset += 2
	if len(data)-offset < nameLen {
		return PacketTypeEntry{}, 0, fmt.Errorf("PacketTypeEntry name truncated")
	}
	name := string(data[offset : offset+nameLen])
	offset += nameLen
	if len(data)-offset < 1 {
		return PacketTypeEntry{}, 0, fmt.Errorf("PacketTypeEntry truncated at descLen")
	}
	descLen := int(data[offset])
	offset++
	if len(data)-offset < descLen {
		return PacketTypeEntry{}, 0, fmt.Errorf("PacketTypeEntry description truncated")
	}
	desc := string(data[offset : offset+descLen])
	return PacketTypeEntry{packetID, name, desc}, offset + descLen, nil
}

// PacketTypeRegistry is the payload for PacketTypeRegistryType.
//
// Sent by the host to advertise application-defined packet types.
type PacketTypeRegistry struct {
	Entries []PacketTypeEntry
}

func (p PacketTypeRegistry) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, uint16(len(p.Entries)))
	for _, e := range p.Entries {
		eb, err := e.MarshalBinary()
		if err != nil {
			return nil, err
		}
		buf = append(buf, eb...)
	}
	return buf, nil
}

func UnmarshalPacketTypeRegistry(data []byte) (PacketTypeRegistry, error) {
	if len(data) < 2 {
		return PacketTypeRegistry{}, fmt.Errorf("PacketTypeRegistry too short")
	}
	count := int(binary.LittleEndian.Uint16(data[0:2]))
	if count > maxPacketCount {
		return PacketTypeRegistry{}, fmt.Errorf("too many packet type entries: %d", count)
	}
	entries := make([]PacketTypeEntry, 0, count)
	offset := 2
	for i := 0; i < count; i++ {
		entry, next, err := unmarshalPacketTypeEntry(data, offset)
		if err != nil {
			return PacketTypeRegistry{}, err
		}
		entries = append(entries, entry)
		offset = next
	}
	return PacketTypeRegistry{entries}, nil
}

// HostRegister is the payload for HostRegisterType.
//
// Sent by the host to register a new session with the relay.
type HostRegister struct {
	SessionID int32
	HostToken uint64
}

func (p HostRegister) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(p.SessionID))
	binary.LittleEndian.PutUint64(buf[4:12], p.HostToken)
	return buf, nil
}

func UnmarshalHostRegister(data []byte) (HostRegister, error) {
	if len(data) < 12 {
		return HostRegister{}, fmt.Errorf("HostRegister too short: %d", len(data))
	}
	return HostRegister{
		SessionID: int32(binary.LittleEndian.Uint32(data[0:4])),
		HostToken: binary.LittleEndian.Uint64(data[4:12]),
	}, nil
}

// Ping is the payload for PingType.
//
// Carries the sender's timestamp so the receiver can echo it back.
type Ping struct {
	Timestamp uint64
}

func (p Ping) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, p.Timestamp)
	return buf, nil
}

func UnmarshalPing(data []byte) (Ping, error) {
	if len(data) < 8 {
		return Ping{}, fmt.Errorf("Ping too short")
	}
	return Ping{Timestamp: binary.LittleEndian.Uint64(data[0:8])}, nil
}

// Pong is the payload for PongType.
//
// Echoes the Ping.Timestamp so the sender can compute round-trip time.
type Pong struct {
	OriginalTimestamp uint64
}

func (p Pong) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, p.OriginalTimestamp)
	return buf, nil
}

func UnmarshalPong(data []byte) (Pong, error) {
	if len(data) < 8 {
		return Pong{}, fmt.Errorf("Pong too short")
	}
	return Pong{OriginalTimestamp: binary.LittleEndian.Uint64(data[0:8])}, nil
}

// DisconnectNotice is the payload for DisconnectNoticeType.
//
// Empty payload; the sender is identified by the header ClientID.
type DisconnectNotice struct{}

func (p DisconnectNotice) MarshalBinary() ([]byte, error) {
	return []byte{}, nil
}

func UnmarshalDisconnectNotice(data []byte) (DisconnectNotice, error) {
	return DisconnectNotice{}, nil
}

// Ack is the payload for AckType.
//
// Acknowledges one or more reliably-delivered packets by sequence number.
type Ack struct {
	Sequences []uint16
}

func (p Ack) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 2+2*len(p.Sequences))
	binary.LittleEndian.PutUint16(buf[0:2], uint16(len(p.Sequences)))
	for i, s := range p.Sequences {
		binary.LittleEndian.PutUint16(buf[2+2*i:4+2*i], s)
	}
	return buf, nil
}

func UnmarshalAck(data []byte) (Ack, error) {
	if len(data) < 2 {
		return Ack{}, fmt.Errorf("Ack too short")
	}
	count := int(binary.LittleEndian.Uint16(data[0:2]))
	if count > maxPacketCount {
		return Ack{}, fmt.Errorf("too many ack sequences: %d", count)
	}
	if len(data) < 2+count*2 {
		return Ack{}, fmt.Errorf("Ack truncated")
	}
	seqs := make([]uint16, count)
	for i := 0; i < count; i++ {
		seqs[i] = binary.LittleEndian.Uint16(data[2+2*i : 4+2*i])
	}
	return Ack{seqs}, nil
}

// ReconnectRequest is the payload for ReconnectRequestType.
//
// Sent by a client to rejoin a session using a previously issued token.
type ReconnectRequest struct {
	Token            uint64
	SessionID        int32
	PreviousClientID uint8
}

func (p ReconnectRequest) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 13)
	binary.LittleEndian.PutUint64(buf[0:8], p.Token)
	binary.LittleEndian.PutUint32(buf[8:12], uint32(p.SessionID))
	buf[12] = p.PreviousClientID
	return buf, nil
}

func UnmarshalReconnectRequest(data []byte) (ReconnectRequest, error) {
	if len(data) < 13 {
		return ReconnectRequest{}, fmt.Errorf("ReconnectRequest too short: %d", len(data))
	}
	return ReconnectRequest{
		Token:            binary.LittleEndian.Uint64(data[0:8]),
		SessionID:        int32(binary.LittleEndian.Uint32(data[8:12])),
		PreviousClientID: data[12],
	}, nil
}

// GamePacket is the payload for any GamePacketType (type byte >= 0x10).
//
// The payload is opaque raw bytes; interpretation is entirely application-defined.
type GamePacket struct {
	RawPayload []byte
}

func (p GamePacket) MarshalBinary() ([]byte, error) {
	return p.RawPayload, nil
}

func UnmarshalGamePacket(data []byte) (GamePacket, error) {
	buf := make([]byte, len(data))
	copy(buf, data)
	return GamePacket{RawPayload: buf}, nil
}

// NeonPacket is a fully parsed Neon protocol packet.
//
// Consists of a fixed 8-byte PacketHeader followed by a variable-length payload.
type NeonPacket struct {
	Header  PacketHeader
	Payload Payload
}

// NewNeonPacket is a convenience constructor that fills the header magic and version.
func NewNeonPacket(packetType PacketType, sequence uint16, clientID, destID uint8, payload Payload) NeonPacket {
	return NeonPacket{
		Header:  NewPacketHeader(uint8(packetType), sequence, clientID, destID),
		Payload: payload,
	}
}

// ToBytes serialises the complete packet to bytes.
func (p NeonPacket) ToBytes() ([]byte, error) {
	headerBytes, err := p.Header.MarshalBinary()
	if err != nil {
		return nil, err
	}
	payloadBytes, err := p.Payload.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return append(headerBytes, payloadBytes...), nil
}

// NeonPacketFromBytes parses a complete packet from data.
func NeonPacketFromBytes(data []byte) (NeonPacket, error) {
	if len(data) < HeaderSize {
		return NeonPacket{}, fmt.Errorf("packet too short: %d", len(data))
	}
	header, err := UnmarshalPacketHeader(data)
	if err != nil {
		return NeonPacket{}, err
	}
	payloadBytes := data[HeaderSize:]
	ptype, err := PacketTypeFromByte(header.PacketType)
	if err != nil {
		return NeonPacket{}, err
	}

	var payload Payload
	switch ptype {
	case ConnectRequestType:
		payload, err = UnmarshalConnectRequest(payloadBytes)
	case ConnectAcceptType:
		payload, err = UnmarshalConnectAccept(payloadBytes)
	case ConnectDenyType:
		payload, err = UnmarshalConnectDeny(payloadBytes)
	case SessionConfigType:
		payload, err = UnmarshalSessionConfig(payloadBytes)
	case PacketTypeRegistryType:
		payload, err = UnmarshalPacketTypeRegistry(payloadBytes)
	case HostRegisterType:
		payload, err = UnmarshalHostRegister(payloadBytes)
	case PingType:
		payload, err = UnmarshalPing(payloadBytes)
	case PongType:
		payload, err = UnmarshalPong(payloadBytes)
	case DisconnectNoticeType:
		payload, err = UnmarshalDisconnectNotice(payloadBytes)
	case AckType:
		payload, err = UnmarshalAck(payloadBytes)
	case ReconnectRequestType:
		payload, err = UnmarshalReconnectRequest(payloadBytes)
	case GamePacketType:
		payload, err = UnmarshalGamePacket(payloadBytes)
	default:
		return NeonPacket{}, fmt.Errorf("unhandled packet type: %v", ptype)
	}
	if err != nil {
		return NeonPacket{}, err
	}
	return NeonPacket{header, payload}, nil
}
