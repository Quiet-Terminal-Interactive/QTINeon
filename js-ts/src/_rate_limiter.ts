const VIOLATION_THRESHOLD = 10;

export class RateLimiter {
  private tokens: number;
  private readonly maxTokens: number;
  private lastRefill: number;
  private violations = 0;
  private lastAccessMs: number;

  constructor(maxTokensPerSecond: number) {
    this.maxTokens = maxTokensPerSecond;
    this.tokens = maxTokensPerSecond;
    this.lastRefill = Date.now();
    this.lastAccessMs = this.lastRefill;
  }

  allowPacket(): boolean {
    const now = Date.now();
    this.lastAccessMs = now;
    this._refill(now);
    if (this.tokens > 0) {
      this.tokens--;
      return true;
    }
    this.violations++;
    return false;
  }

  get isThrottled(): boolean {
    return this.violations > VIOLATION_THRESHOLD;
  }

  get lastAccess(): number {
    return this.lastAccessMs;
  }

  private _refill(now: number): void {
    if (now - this.lastRefill >= 1000) {
      this.tokens = this.maxTokens;
      this.lastRefill = now;
      if (this.violations > 0) this.violations--;
    }
  }
}
