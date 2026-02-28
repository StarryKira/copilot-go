package instance

import (
	"sync"
	"sync/atomic"

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

// instances and mu are defined in manager.go but referenced here
var (
	instances map[string]*ProxyInstance
	mu        sync.RWMutex
)

func init() {
	instances = make(map[string]*ProxyInstance)
}
