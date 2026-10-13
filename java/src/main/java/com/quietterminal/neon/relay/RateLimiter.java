package com.quietterminal.neon.relay;

final class RateLimiter {

    private static final int VIOLATION_THRESHOLD = 10;

    private int tokens;
    private final int maxTokens;
    private long lastRefillMs;
    private int violations;
    private long lastAccessMs;

    RateLimiter(int maxTokensPerSecond) {
        this.maxTokens = maxTokensPerSecond;
        this.tokens = maxTokensPerSecond;
        this.lastRefillMs = System.currentTimeMillis();
        this.lastAccessMs = this.lastRefillMs;
    }

    synchronized boolean allowPacket() {
        long now = System.currentTimeMillis();
        lastAccessMs = now;
        refill(now);
        if (tokens > 0) {
            tokens--;
            return true;
        }
        violations++;
        return false;
    }

    synchronized long lastAccessMs() {
        return lastAccessMs;
    }

    boolean isThrottled() {
        return violations > VIOLATION_THRESHOLD;
    }

    private void refill(long now) {
        if (now - lastRefillMs >= 1000) {
            tokens = maxTokens;
            lastRefillMs = now;
            if (violations > 0)
                violations--;
        }
    }
}
