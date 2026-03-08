package instance

import (
	"math"
	"os"
	"strconv"
	"sync"
	"time"
)

// TokenBucket implements a token-bucket rate limiter.
type TokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// NewTokenBucket creates a rate limiter with the given requests-per-minute limit.
func NewTokenBucket(rpm int) *TokenBucket {
	rate := float64(rpm) / 60.0 // tokens per second
	return &TokenBucket{
		tokens:     float64(rpm), // start full
		maxTokens:  float64(rpm),
		refillRate: rate,
		lastRefill: time.Now(),
	}
}

// Allow checks whether a request is allowed. Returns (allowed, retryAfterSeconds).
func (tb *TokenBucket) Allow() (bool, float64) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens = math.Min(tb.maxTokens, tb.tokens+elapsed*tb.refillRate)
	tb.lastRefill = now

	if tb.tokens >= 1.0 {
		tb.tokens--
		return true, 0
	}

	// Calculate how long until 1 token is available.
	waitSeconds := (1.0 - tb.tokens) / tb.refillRate
	return false, math.Ceil(waitSeconds)
}

// RateLimiterManager manages global and per-account rate limiters.
type RateLimiterManager struct {
	mu              sync.RWMutex
	globalLimiter   *TokenBucket // nil if disabled
	accountLimiters map[string]*TokenBucket
	perAccountRPM   int // from PoolConfig, 0 = disabled
}

var rateLimiter = &RateLimiterManager{
	accountLimiters: make(map[string]*TokenBucket),
}

// InitRateLimiter initializes the global rate limiter from environment variables.
func InitRateLimiter() {
	rpmStr := os.Getenv("RATE_LIMIT_RPM")
	if rpmStr == "" {
		return
	}
	rpm, err := strconv.Atoi(rpmStr)
	if err != nil || rpm <= 0 {
		return
	}
	rateLimiter.mu.Lock()
	rateLimiter.globalLimiter = NewTokenBucket(rpm)
	rateLimiter.mu.Unlock()
}

// SetPerAccountRPM updates the per-account rate limit. Called when pool config changes.
func SetPerAccountRPM(rpm int) {
	rateLimiter.mu.Lock()
	defer rateLimiter.mu.Unlock()
	rateLimiter.perAccountRPM = rpm
	// Reset existing per-account limiters so they pick up the new rate.
	rateLimiter.accountLimiters = make(map[string]*TokenBucket)
}

// CheckRateLimit checks both global and per-account rate limits.
// Returns (allowed, retryAfterSeconds).
func CheckRateLimit(accountID string) (bool, float64) {
	rateLimiter.mu.RLock()
	globalLim := rateLimiter.globalLimiter
	perRPM := rateLimiter.perAccountRPM
	rateLimiter.mu.RUnlock()

	// Check global limit first.
	if globalLim != nil {
		if ok, retryAfter := globalLim.Allow(); !ok {
			return false, retryAfter
		}
	}

	// Check per-account limit.
	if perRPM > 0 && accountID != "" {
		lim := getOrCreateAccountLimiter(accountID, perRPM)
		if ok, retryAfter := lim.Allow(); !ok {
			return false, retryAfter
		}
	}

	return true, 0
}

func getOrCreateAccountLimiter(accountID string, rpm int) *TokenBucket {
	rateLimiter.mu.RLock()
	lim, ok := rateLimiter.accountLimiters[accountID]
	rateLimiter.mu.RUnlock()
	if ok {
		return lim
	}

	rateLimiter.mu.Lock()
	defer rateLimiter.mu.Unlock()
	if lim, ok = rateLimiter.accountLimiters[accountID]; ok {
		return lim
	}
	lim = NewTokenBucket(rpm)
	rateLimiter.accountLimiters[accountID] = lim
	return lim
}
