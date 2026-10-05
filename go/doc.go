// Package qtineon is a minimal, game-agnostic, relay-based UDP multiplayer protocol library.
//
// Clients never communicate directly.
// The relay routes packets by destination ID in the packet header, keeping NAT traversal trivial and host addresses private.
// The host is just another participant; it has no special network position, only a special protocol role.
//
// # Quick start
//
// Run a relay:
//
//	relay := qtineon.NewRelay("0.0.0.0", qtineon.DefaultConfig())
//	go relay.StartAndRun()
//
// Start a host:
//
//	host := qtineon.NewHost(42, "relay.example.com:7777", qtineon.DefaultConfig())
//	host.SetClientConnectCallback(func(id uint8, name string, sessionID int32) {
//		log.Printf("%s joined as %d", name, id)
//	})
//	go host.StartAndRun()
//
// Connect a client:
//
//	client := qtineon.NewClient("player1", qtineon.DefaultConfig())
//	if err := client.Connect(42, "relay.example.com:7777"); err == nil {
//		go client.Run()
//	}
//
// Send a packet:
//
//	data := encodePosition(x, y, z)
//	client.SendPacket(data, qtineon.PacketTypeGamePacket, 0) // 0 = broadcast
//
// See PROTOCOL.md, ARCHITECTURE.md and CONFIGURATION.md in the repository root for the wire format, topology, and configuration reference shared by every language implementation.
package qtineon
