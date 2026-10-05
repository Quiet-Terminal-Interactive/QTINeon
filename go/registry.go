package qtineon

import "fmt"

// GamePacketDescriptor is metadata for a single registered game packet type.
type GamePacketDescriptor struct {
	// PacketID is the wire type byte (must be >= 0x10).
	PacketID    uint8
	Name        string
	Description string
}

// GamePacketRegistry is a registry of application-defined game packet types.
//
// Registered types are advertised to connecting clients via a PacketTypeRegistry packet.
//
//	registry := qtineon.NewGamePacketRegistry()
//	registry.Register(0x10, "POSITION", "Player position update")
//	registry.Register(0x11, "CHAT", "Chat message")
//
//	host.SetGamePacketRegistry(registry)
type GamePacketRegistry struct {
	descriptors []GamePacketDescriptor
}

// NewGamePacketRegistry creates an empty GamePacketRegistry.
func NewGamePacketRegistry() *GamePacketRegistry {
	return &GamePacketRegistry{}
}

// Register adds a game packet type to the registry.
//
// packetID must be >= 0x10. name should be at most 64 characters and description at most 256 characters.
func (r *GamePacketRegistry) Register(packetID uint8, name, description string) error {
	if packetID < 0x10 {
		return fmt.Errorf("packetID must be >= 0x10, got 0x%02x", packetID)
	}
	r.descriptors = append(r.descriptors, GamePacketDescriptor{packetID, name, description})
	return nil
}

// BuildRegistry builds the wire-format PacketTypeRegistry payload from all registered types.
func (r *GamePacketRegistry) BuildRegistry() PacketTypeRegistry {
	entries := make([]PacketTypeEntry, 0, len(r.descriptors))
	for _, d := range r.descriptors {
		entries = append(entries, PacketTypeEntry{d.PacketID, d.Name, d.Description})
	}
	return PacketTypeRegistry{entries}
}

// IsEmpty reports whether no types have been registered.
func (r *GamePacketRegistry) IsEmpty() bool {
	return len(r.descriptors) == 0
}
