## Token bucket rate limiter for a single peer.
##
## Refills to [code]max_packets_per_second[/code] tokens once per second.
## Each call to [method allow] consumes one token. When the bucket is empty the call
## returns [code]false[/code] and the violation counter increments.
## After 10 consecutive violation seconds the peer is considered throttled.

var _max_tokens: float
var _tokens: float
var _last_check: int
var _last_access: int
var _violations: int = 0
var _throttled: bool = false


## Creates a rate limiter with the given packet rate.
## [param max_packets_per_second] Number of packets allowed per second.
func _init(max_packets_per_second: int) -> void:
	_max_tokens = float(max_packets_per_second)
	_tokens = _max_tokens
	_last_check = Time.get_ticks_msec()
	_last_access = _last_check


## Returns [code]true[/code] if this packet is within the allowed rate.
##
## Refills the token bucket when a full second has elapsed since the last check.
func allow() -> bool:
	var now := Time.get_ticks_msec()
	_last_access = now
	if now - _last_check >= 1000:
		_last_check = now
		_tokens = _max_tokens
		_violations = max(0, _violations - 1)
		_throttled = _violations > 10

	if _tokens >= 1.0:
		_tokens -= 1.0
		return true

	_violations += 1
	_throttled = _violations > 10
	return false


## Returns [code]true[/code] if this peer has sustained more than 10 seconds of violations.
func is_throttled() -> bool:
	return _throttled

## Returns the [method Time.get_ticks_msec] timestamp of the most recent [method allow] call.
func last_access() -> int:
	return _last_access