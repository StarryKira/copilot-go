package instance

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"copilot-go/store"
)

var rrIndex atomic.Int64

// SelectAccount picks an account using the specified strategy.
// exclude contains account IDs to skip (e.g., on retry).
func SelectAccount(strategy string, exclude map[string]bool) (*store.Account, error) {
	accounts, err := store.GetEnabledAccounts()
	if err != nil {
		return nil, err
	}

	// Filter out excluded and non-running accounts
	var available []store.Account
	mu.RLock()
	for _, a := range accounts {
		if exclude != nil && exclude[a.ID] {
			continue
		}
		if inst, ok := instances[a.ID]; ok && inst.Status == "running" {
			available = append(available, a)
		}
	}
	mu.RUnlock()

	if len(available) == 0 {
		return nil, nil
	}

	switch strategy {
	case "priority":
		return selectByPriority(available), nil
	case "least-used":
		return selectLeastUsed(available), nil
	case "smart":
		return selectSmart(available), nil
	default: // round-robin
		return selectRoundRobin(available), nil
	}
}

func selectRoundRobin(accounts []store.Account) *store.Account {
	idx := rrIndex.Add(1) - 1
	selected := accounts[int(idx)%len(accounts)]
	return &selected
}

func selectByPriority(accounts []store.Account) *store.Account {
	// Higher priority value = higher priority
	best := accounts[0]
	for _, a := range accounts[1:] {
		if a.Priority > best.Priority {
			best = a
		}
	}
	return &best
}

// selectLeastUsed picks the account with the fewest requests in the current window.
func selectLeastUsed(accounts []store.Account) *store.Account {
	best := &accounts[0]
	bestCount := GetWindowRequestCount(accounts[0].ID)

	for i := 1; i < len(accounts); i++ {
		count := GetWindowRequestCount(accounts[i].ID)
		if count < bestCount {
			bestCount = count
			best = &accounts[i]
		}
	}
	return best
}

// selectSmart picks the account with the lowest weighted score.
// Score = requestCount + penalty if recently 429'd.
// The 429 penalty decays over 5 minutes.
func selectSmart(accounts []store.Account) *store.Account {
	const penaltyDuration = 5 * time.Minute
	const maxPenalty = 1000.0

	now := time.Now()
	best := &accounts[0]
	bestScore := math.MaxFloat64

	for i := range accounts {
		count := float64(GetWindowRequestCount(accounts[i].ID))
		penalty := 0.0

		last429 := GetLast429Time(accounts[i].ID)
		if !last429.IsZero() {
			elapsed := now.Sub(last429)
			if elapsed < penaltyDuration {
				// Linear decay: full penalty at 0s, 0 at penaltyDuration.
				ratio := 1.0 - elapsed.Seconds()/penaltyDuration.Seconds()
				penalty = maxPenalty * ratio
			}
		}

		score := count + penalty
		if score < bestScore {
			bestScore = score
			best = &accounts[i]
		}
	}
	return best
}

// instances and mu are defined in manager.go but referenced here
var (
	instances map[string]*ProxyInstance
	mu        sync.RWMutex
)

func init() {
	instances = make(map[string]*ProxyInstance)
}
