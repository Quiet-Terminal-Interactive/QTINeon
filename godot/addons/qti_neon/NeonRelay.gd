## UDP/DTLS relay server that routes packets between hosts and clients.
##
## The relay does not inspect game packet payloads; it only reads protocol headers
## to determine routing destinations. One relay can host multiple concurrent sessions.
##
## Call [method start_and_run] on a dedicated thread, or call [method start] and then
## drive the relay manually by calling [method process] in a loop.
class_name NeonRelay
extends RefCounted

const _Proto = preload("res://addons/qti_neon/_protocol.gd")
const _RelSock = preload("res://addons/qti_neon/_relay_socket.gd")
const _SessMod = preload("res://addons/qti_neon/_session.gd")
const _RateMod = preload("res://addons/qti_neon/_rate_limiter.gd")

var _bind_address: String
var _config: NeonConfig

var _socket: _RelSock = null
var _sessions: _SessMod = null
var _running: bool = false

var _pending_connections: Dictionary = {}
var _pending_by_session: Dictionary = {}
var _pending_reconnects: Dictionary = {}
var _rate_limiters: Dictionary = {}

var _last_cleanup: int = 0

## Creates a new relay.
## [param bind_address] The local address to bind to. Defaults to [code]0.0.0.0[/code].
## [param config] Optional configuration; defaults are used if [code]null[/code].
func _init(bind_address: String = "0.0.0.0", config: NeonConfig = null) -> void:
	_bind_address = bind_address
	_config = config if config != null else NeonConfig.new()

## Starts the relay and blocks in the processing loop until [method stop] is called.
##
## Run on a dedicated thread.
func start_and_run() -> void:
	if not _do_start():
		return
	while _running:
		process()
		OS.delay_msec(_config.relay_main_loop_sleep_ms)

## Binds the relay socket without blocking.
##
## Returns [code]true[/code] on success. After this returns, call [method process]
## in a loop to route packets.
func start() -> bool:
	return _do_start()

func _do_start() -> bool:
	_socket = _RelSock.new()
	_sessions = _SessMod.new()
	var err := _socket.bind(_config.relay_port, _config.dtls_config)
	if err != OK:
		push_error("NeonRelay: bind failed on port %d: %s" % [_config.relay_port, error_string(err)])
		return false
	_running = true
	_last_cleanup = Time.get_ticks_msec()
	return true

## Stops the relay and closes the socket.
func stop() -> void:
	_running = false
	if _socket != null:
		_socket.stop()
		_socket = null

## Returns [code]true[/code] if the relay is bound and processing.
func is_running() -> bool:
	return _running

## Receives all pending packets and routes them to their destinations.
##
## Also runs periodic cleanup of stale sessions and timed-out pending connections.
## Called automatically by [method start_and_run].
func process() -> void:
	if not _running:
		return
	var packets := _socket.receive_all(_config.buffer_size, _config.enforce_buffer_size)
	for pkt in packets:
		_handle_raw(pkt["data"] as PackedByteArray, pkt["peer_key"] as String)
	_perform_cleanup()

func _handle_raw(data: PackedByteArray, peer_key: String) -> void:
	if not _check_rate(peer_key):
		return
	var hdr := _Proto.parse_header(data)
	if hdr.is_empty():
		return
	_sessions.touch_peer(peer_key)
	var payload := _Proto.payload_of(data)
	match hdr["type"]:
		_Proto.PT_HOST_REGISTER:
			_on_host_register(payload, peer_key)
		_Proto.PT_CONNECT_REQUEST:
			_on_connect_request(data, payload, peer_key)
		_Proto.PT_CONNECT_ACCEPT:
			_on_connect_accept(data, payload, peer_key)
		_Proto.PT_CONNECT_DENY:
			_on_connect_deny(data, peer_key)
		_Proto.PT_RECONNECT_REQUEST:
			_on_reconnect_request(data, payload, peer_key)
		_Proto.PT_DISCONNECT_NOTICE:
			_on_disconnect_notice(data, peer_key)
		_:
			_route_opaque(data, hdr, peer_key)

func _check_rate(peer_key: String) -> bool:
	if not _rate_limiters.has(peer_key):
		if _rate_limiters.size() >= _config.max_rate_limiters:
			return false
		_rate_limiters[peer_key] = _RateMod.new(_config.max_packets_per_second)
	return _rate_limiters[peer_key].allow()

func _on_host_register(payload: PackedByteArray, peer_key: String) -> void:
	var p := _Proto.parse_host_register(payload)
	if p.is_empty():
		return
	var sid: int = p["session_id"]
	if sid <= 0:
		return
	_sessions.register_host(sid, peer_key)
	_pending_by_session[sid] = []
	var accept_payload := _Proto.build_connect_accept(1, sid, p["host_token"])
	var pkt := _Proto.build_packet(_Proto.PT_CONNECT_ACCEPT, 0, 0, 1, accept_payload)
	_socket.send(pkt, peer_key)

func _on_connect_request(data: PackedByteArray, payload: PackedByteArray, peer_key: String) -> void:
	var p := _Proto.parse_connect_request(payload)
	if p.is_empty():
		return
	var sid: int = p["session_id"]
	if not _sessions.has_session(sid):
		_deny_to(peer_key, "Session not found")
		return
	if _pending_connections.size() >= _config.max_pending_connections:
		_deny_to(peer_key, "Server busy")
		return
	if _sessions.total_client_count() >= _config.max_total_connections:
		_deny_to(peer_key, "Server full")
		return
	var s := _sessions.get_session(sid)
	if s != null and s.clients.size() >= _config.max_clients_per_session:
		_deny_to(peer_key, "Session full")
		return
	_pending_connections[peer_key] = {"session_id": sid, "timestamp": Time.get_ticks_msec()}
	if not _pending_by_session.has(sid):
		_pending_by_session[sid] = []
	(_pending_by_session[sid] as Array).append(peer_key)
	var hk := _sessions.host_peer_key(sid)
	if hk != "":
		_socket.send(data, hk)

func _on_connect_accept(data: PackedByteArray, payload: PackedByteArray, peer_key: String) -> void:
	var p := _Proto.parse_connect_accept(payload)
	if p.is_empty():
		return
	var sid: int = _sessions.session_id_for_peer(peer_key)
	if sid == -1:
		return
	var cid: int = p["client_id"]
	if cid == 1:
		return
	var rk := "%d:%d" % [sid, cid]
	if _pending_reconnects.has(rk):
		var rec: Dictionary = _pending_reconnects[rk]
		_pending_reconnects.erase(rk)
		var new_key: String = rec["new_peer_key"]
		_sessions.update_client_peer(sid, cid, new_key)
		_socket.send(data, new_key)
		return
	if not _pending_by_session.has(sid):
		return
	var queue: Array = _pending_by_session[sid]
	if queue.is_empty():
		return
	var cpk: String = queue.pop_front()
	_pending_connections.erase(cpk)
	_sessions.register_client(sid, cid, cpk)
	_socket.send(data, cpk)

func _on_connect_deny(data: PackedByteArray, peer_key: String) -> void:
	var sid: int = _sessions.session_id_for_peer(peer_key)
	if sid == -1:
		var hdr := _Proto.parse_header(data)
		if not hdr.is_empty():
			_route_opaque(data, hdr, peer_key)
		return
	if not _pending_by_session.has(sid):
		return
	var queue: Array = _pending_by_session[sid]
	if queue.is_empty():
		return
	var cpk: String = queue.pop_front()
	_pending_connections.erase(cpk)
	_socket.send(data, cpk)

func _on_reconnect_request(data: PackedByteArray, payload: PackedByteArray, peer_key: String) -> void:
	var p := _Proto.parse_reconnect_request(payload)
	if p.is_empty():
		return
	var sid: int = p["session_id"]
	var cid: int = p["previous_client_id"]
	var rk := "%d:%d" % [sid, cid]
	var new_reconnect := not _pending_reconnects.has(rk)
	if new_reconnect and _pending_reconnects.size() >= _config.max_pending_connections:
		_deny_to(peer_key, "Relay busy")
		return
	_pending_reconnects[rk] = {"new_peer_key": peer_key, "timestamp": Time.get_ticks_msec()}
	if new_reconnect:
		var hk := _sessions.host_peer_key(sid)
		if hk != "":
			_socket.send(data, hk)

func _on_disconnect_notice(data: PackedByteArray, peer_key: String) -> void:
	if _pending_connections.has(peer_key):
		var pc: Dictionary = _pending_connections[peer_key]
		var psid: int = pc["session_id"]
		if _pending_by_session.has(psid):
			(_pending_by_session[psid] as Array).erase(peer_key)
		_pending_connections.erase(peer_key)
		_rate_limiters.erase(peer_key)
		_socket.remove_peer(peer_key)

	var sid: int = _sessions.session_id_for_peer(peer_key)
	if sid == -1:
		return
	var all_keys := _sessions.all_peer_keys_in_session(sid)
	for k in all_keys:
		if k != peer_key:
			_socket.send(data, k)
	_evict_peer(sid, peer_key)

func _evict_peer(sid: int, peer_key: String) -> void:
	var s := _sessions.get_session(sid)
	if s == null:
		return
	if s.host_peer_key == peer_key:
		_sessions.unregister_session(sid)
		_pending_by_session.erase(sid)
	else:
		for cid in s.clients.keys():
			if s.clients[cid] == peer_key:
				_sessions.remove_client(sid, cid)
				break
	_rate_limiters.erase(peer_key)
	_socket.remove_peer(peer_key)

func _route_opaque(data: PackedByteArray, hdr: Dictionary, sender_key: String) -> void:
	var sid: int = _sessions.session_id_for_peer(sender_key)
	if sid == -1:
		return
	var dest: int = hdr["destination"]
	match dest:
		0:
			for k in _sessions.all_peer_keys_in_session(sid):
				if k != sender_key:
					_socket.send(data, k)
		1:
			var hk := _sessions.host_peer_key(sid)
			if hk != "":
				_socket.send(data, hk)
		_:
			var ck := _sessions.client_peer_key(sid, dest)
			if ck != "":
				_socket.send(data, ck)

func _deny_to(peer_key: String, reason: String) -> void:
	var deny_payload := _Proto.build_connect_deny(reason)
	var pkt := _Proto.build_packet(_Proto.PT_CONNECT_DENY, 0, 0, 0, deny_payload)
	_socket.send(pkt, peer_key)

func _perform_cleanup() -> void:
	var now := Time.get_ticks_msec()
	if now - _last_cleanup < _config.relay_cleanup_interval_ms:
		return
	_last_cleanup = now

	for sid in _sessions.collect_stale_sessions(_config.relay_client_timeout_ms):
		_sessions.unregister_session(sid)
		_pending_by_session.erase(sid)

	for entry in _sessions.collect_stale_clients(_config.relay_client_timeout_ms):
		var sid: int = entry["session_id"]
		var cid: int = entry["client_id"]
		var ck := _sessions.client_peer_key(sid, cid)
		_sessions.remove_client(sid, cid)
		if ck != "":
			_rate_limiters.erase(ck)
			_socket.remove_peer(ck)

	var stale_c: Array = []
	for pk in _pending_connections.keys():
		if now - (_pending_connections[pk] as Dictionary)["timestamp"] > _config.relay_client_timeout_ms:
			stale_c.append(pk)
	for pk in stale_c:
		var sid: int = (_pending_connections[pk] as Dictionary)["session_id"]
		if _pending_by_session.has(sid):
			(_pending_by_session[sid] as Array).erase(pk)
		_pending_connections.erase(pk)
		_rate_limiters.erase(pk)
		_socket.remove_peer(pk)

	var stale_r: Array = []
	for rk in _pending_reconnects.keys():
		if now - (_pending_reconnects[rk] as Dictionary)["timestamp"] > _config.relay_client_timeout_ms:
			stale_r.append(rk)
	for rk in stale_r:
		_pending_reconnects.erase(rk)

	var stale_l: Array = []
	for pk in _rate_limiters.keys():
		if now - (_rate_limiters[pk] as _RateMod).last_access() > _config.relay_client_timeout_ms:
			stale_l.append(pk)
	for pk in stale_l:
		_rate_limiters.erase(pk)