import { NeonConfig } from "./_config";
import { NeonSocket, Address, addressKey } from "./_socket";
import { SessionManager } from "./_session";
import { RateLimiter } from "./_rate_limiter";
import {
  NeonPacket,
  PacketType,
  makePacket,
  serializePacket,
  isConnectRequest,
  isConnectAccept,
  isConnectDeny,
  isHostRegister,
  isReconnectRequest,
  isDisconnectNotice,
  ConnectAcceptPayload,
  ConnectDenyPayload,
  DisconnectNoticePayload,
} from "./_protocol";

interface PendingConnection {
  sessionId: number;
  requestedAt: number;
}

interface PendingReconnect {
  sessionId: number;
  clientId: number;
  newAddress: Address;
  requestedAt: number;
}

enum State {
  CREATED = "CREATED",
  RUNNING = "RUNNING",
  STOPPED = "STOPPED",
}

export class NeonRelay {
  private config: NeonConfig;
  private socket: NeonSocket;
  private sessionManager = new SessionManager();
  private rateLimiters = new Map<string, RateLimiter>();
  private pendingConnections = new Map<string, PendingConnection>();
  private pendingBySession = new Map<number, Address[]>();
  private pendingReconnects = new Map<string, PendingReconnect>();
  private lastCleanup = Date.now();
  private cleanupTimer: NodeJS.Timeout | null = null;
  private state = State.CREATED;

  constructor(bindAddress: string, config?: NeonConfig) {
    this.config = config ?? new NeonConfig();
    const [host, portStr] = bindAddress.includes(":")
      ? [bindAddress.slice(0, bindAddress.lastIndexOf(":")), bindAddress.slice(bindAddress.lastIndexOf(":") + 1)]
      : [bindAddress, String(this.config.relayPort)];
    this.socket = new NeonSocket({ address: host, port: parseInt(portStr, 10) }, this.config);
    this.socket.on("packet", (pkt: NeonPacket, addr: Address) => this._onReceive(pkt, addr));
  }

  async start(): Promise<void> {
    if (this.state !== State.CREATED) throw new Error(`Cannot start from state: ${this.state}`);
    await this.socket.waitBound();
    if (this.config.isDtlsEnabled) {
      this.socket.enableServerDtls(this.config.dtlsConfig!);
    }
    this.state = State.RUNNING;
    this.cleanupTimer = setInterval(() => this._performCleanup(), this.config.relayCleanupIntervalMs);
    if (this.cleanupTimer.unref) this.cleanupTimer.unref();
  }

  async startAndRun(): Promise<void> {
    await this.start();
  }

  stop(): void {
    if (this.state !== State.RUNNING) throw new Error(`Cannot stop from state: ${this.state}`);
    this.state = State.STOPPED;
    if (this.cleanupTimer) {
      clearInterval(this.cleanupTimer);
      this.cleanupTimer = null;
    }
    this.socket.close();
  }

  get isRunning(): boolean {
    return this.state === State.RUNNING;
  }

  localAddress(): Address {
    return this.socket.localAddress();
  }

  /**
   * Called for every packet that passes rate limiting, before default handling.
   * The default implementation does nothing.
   *
   * @returns `true` to consume the packet and skip default handling.
   */
  protected onPacket(_packet: NeonPacket, _source: Address): boolean {
    return false;
  }

  /** Sends a packet to `dest`, encrypting via DTLS if a session exists. */
  protected send(packet: NeonPacket, dest: Address): void {
    this.socket.sendPacket(packet, dest);
  }

  private _onReceive(pkt: NeonPacket, source: Address): void {
    if (this.state !== State.RUNNING) return;

    this.sessionManager.updateLastSeen(source);

    const key = addressKey(source);
    let limiter = this.rateLimiters.get(key);
    if (!limiter) {
      if (this.rateLimiters.size >= this.config.maxRateLimiters) {
        return;
      }
      limiter = new RateLimiter(this.config.maxPacketsPerSecond);
      this.rateLimiters.set(key, limiter);
    }
    if (!limiter.allowPacket()) return;

    this._handlePacket(pkt, source);
  }

  private _handlePacket(pkt: NeonPacket, source: Address): void {
    if (this.onPacket(pkt, source)) return;
    const p = pkt.payload;
    if (isHostRegister(p)) {
      this._handleHostRegister(p.sessionId, p.hostToken, source);
    } else if (isConnectRequest(p)) {
      this._handleConnectRequest(p.sessionId, p, pkt.header, source);
    } else if (isConnectAccept(p)) {
      this._handleConnectAccept(p, pkt.header, source);
    } else if (isConnectDeny(p)) {
      this._handleConnectDeny(p, pkt.header, source);
    } else if (isReconnectRequest(p)) {
      this._handleReconnectRequest(p.sessionId, p.previousClientId, p, source);
    } else if (isDisconnectNotice(p)) {
      this._handleDisconnectNotice(source, pkt.header);
    } else {
      this._routePacket(pkt, source);
    }
  }

  private _handleHostRegister(sessionId: number, hostToken: bigint, source: Address): void {
    if (sessionId <= 0) return;
    this.sessionManager.registerHost(sessionId, source);
    const accept: ConnectAcceptPayload = { clientId: 1, sessionId, token: hostToken };
    this.socket.sendPacket(makePacket(PacketType.CONNECT_ACCEPT, 0, 0, 1, accept), source);
  }

  private _handleConnectRequest(
    sessionId: number,
    payload: NeonPacket["payload"],
    header: NeonPacket["header"],
    source: Address
  ): void {
    const host = this.sessionManager.getHost(sessionId);
    if (!host) {
      this._sendDeny(source, "Session not found");
      return;
    }
    if (this.sessionManager.getClientCount(sessionId) >= this.config.maxClientsPerSession) {
      this._sendDeny(source, "Session full");
      return;
    }
    if (this.pendingConnections.size >= this.config.maxPendingConnections) {
      this._sendDeny(source, "Relay busy");
      return;
    }

    const key = addressKey(source);
    this.pendingConnections.set(key, { sessionId, requestedAt: Date.now() });

    let queue = this.pendingBySession.get(sessionId);
    if (!queue) {
      queue = [];
      this.pendingBySession.set(sessionId, queue);
    }
    queue.push(source);

    this.socket.sendPacket(
      makePacket(PacketType.CONNECT_REQUEST, header.sequence, header.clientId, 1, payload),
      host
    );
  }

  private _handleConnectAccept(accept: ConnectAcceptPayload, header: NeonPacket["header"], source: Address): void {
    const { clientId, sessionId } = accept;

    if (clientId === 1) return;

    const reconnectKey = `${sessionId}:${clientId}`;
    const reconnect = this.pendingReconnects.get(reconnectKey);
    if (reconnect) {
      this.pendingReconnects.delete(reconnectKey);
      this.sessionManager.updatePeerAddress(sessionId, clientId, reconnect.newAddress);
      this.socket.sendPacket(
        makePacket(PacketType.CONNECT_ACCEPT, header.sequence, header.clientId, clientId, accept),
        reconnect.newAddress
      );
      return;
    }

    const queue = this.pendingBySession.get(sessionId);
    const clientAddr = queue?.shift();
    if (!clientAddr) return;
    if (queue && queue.length === 0) this.pendingBySession.delete(sessionId);

    this.pendingConnections.delete(addressKey(clientAddr));
    this.sessionManager.registerPeer(sessionId, clientId, clientAddr);
    this.socket.sendPacket(
      makePacket(PacketType.CONNECT_ACCEPT, header.sequence, header.clientId, clientId, accept),
      clientAddr
    );
  }

  private _handleConnectDeny(deny: ConnectDenyPayload, header: NeonPacket["header"], source: Address): void {
    const sessionId = this.sessionManager.getSessionForPeer(source);
    if (sessionId === undefined) return;

    const queue = this.pendingBySession.get(sessionId);
    const clientAddr = queue?.shift();
    if (!clientAddr) return;
    if (queue && queue.length === 0) this.pendingBySession.delete(sessionId);

    this.pendingConnections.delete(addressKey(clientAddr));
    this.socket.sendPacket(
      makePacket(PacketType.CONNECT_DENY, header.sequence, 1, 0, deny),
      clientAddr
    );
  }

  private _handleReconnectRequest(
    sessionId: number,
    previousClientId: number,
    payload: NeonPacket["payload"],
    source: Address
  ): void {
    const host = this.sessionManager.getHost(sessionId);
    if (!host) {
      this._sendDeny(source, "Session not found");
      return;
    }
    const key = `${sessionId}:${previousClientId}`;
    const newReconnect = !this.pendingReconnects.has(key);
    if (newReconnect && this.pendingReconnects.size >= this.config.maxPendingConnections) {
      this._sendDeny(source, "Relay busy");
      return;
    }
    this.pendingReconnects.set(key, {
      sessionId,
      clientId: previousClientId,
      newAddress: source,
      requestedAt: Date.now(),
    });
    if (newReconnect) {
      this.socket.sendPacket(makePacket(PacketType.RECONNECT_REQUEST, 0, previousClientId, 1, payload), host);
    }
  }

  private _handleDisconnectNotice(source: Address, header: NeonPacket["header"]): void {
    const sessionId = this.sessionManager.getSessionForPeer(source);
    if (sessionId !== undefined) {
      const notice: DisconnectNoticePayload = { _tag: "disconnect" };
      const raw = serializePacket(makePacket(PacketType.DISCONNECT_NOTICE, header.sequence, header.clientId, 0, notice));
      for (const peer of this.sessionManager.getAllPeersExcept(sessionId, source)) {
        this.socket.send(raw, peer);
      }
    }
    this.sessionManager.removePeer(source);
    this.pendingConnections.delete(addressKey(source));
    this.rateLimiters.delete(addressKey(source));
    this.socket.removeDtlsSession(source);
  }

  private _routePacket(pkt: NeonPacket, source: Address): void {
    const sessionId = this.sessionManager.getSessionForPeer(source);
    if (sessionId === undefined) return;

    const destId = pkt.header.destinationId;
    const raw = serializePacket(pkt);

    if (destId === 0) {
      for (const peer of this.sessionManager.getAllPeersExcept(sessionId, source)) {
        this.socket.send(raw, peer);
      }
    } else {
      const dest = this.sessionManager.getPeerAddress(sessionId, destId);
      if (dest) this.socket.send(raw, dest);
    }
  }

  private _sendDeny(dest: Address, reason: string): void {
    const p: ConnectDenyPayload = { reason };
    this.socket.sendPacket(makePacket(PacketType.CONNECT_DENY, 0, 0, 0, p), dest);
  }

  private _performCleanup(): void {
    if (this.state !== State.RUNNING) return;
    this.sessionManager.removeStale(this.config.relayClientTimeoutMs);

    const cutoff = Date.now() - this.config.relayClientTimeoutMs;
    for (const [key, conn] of this.pendingConnections) {
      if (conn.requestedAt < cutoff) {
        this.pendingConnections.delete(key);
        const queue = this.pendingBySession.get(conn.sessionId);
        if (queue) {
          const idx = queue.findIndex((a) => addressKey(a) === key);
          if (idx >= 0) queue.splice(idx, 1);
          if (queue.length === 0) this.pendingBySession.delete(conn.sessionId);
        }
      }
    }

    for (const [key, rc] of this.pendingReconnects) {
      if (rc.requestedAt < cutoff) this.pendingReconnects.delete(key);
    }

    const now = Date.now();
    for (const [k, v] of this.rateLimiters) {
      if (now - v.lastAccess > this.config.relayClientTimeoutMs) {
        this.rateLimiters.delete(k);
      }
    }
  }
}
