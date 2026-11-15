package router

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"ai-proxy/internal/config"
)

type accountIdentity struct {
	origin     string
	format     string
	credential [sha256.Size]byte
	name       string
}

func identifyAccount(provider *config.Provider) accountIdentity {
	key := accountIdentity{
		origin:     upstreamOrigin(provider.BaseURL),
		format:     provider.Format,
		credential: sha256.Sum256([]byte(provider.APIKey)),
	}
	if provider.APIKey == "" {
		key.name = provider.Name
	}
	return key
}

type accountLoad struct {
	active      int
	lastAttempt time.Time
	sequence    uint64
}

type accountCandidate struct {
	provider *config.Provider
	key      accountIdentity
	tried    bool
}

type accountSkip struct {
	index int
	err   error
}

// A format is part of the identity: OpenAI/Responses and Anthropic never share
// load or recency, even when the configured credentials are identical.
type accountScheduler struct {
	mu         sync.Mutex
	loads      map[accountIdentity]*accountLoad
	configured map[accountIdentity]struct{}
	sequence   uint64
}

func newAccountScheduler(cfg *config.Config) *accountScheduler {
	s := &accountScheduler{loads: make(map[accountIdentity]*accountLoad)}
	s.reconcile(cfg)
	return s
}

func (s *accountScheduler) reconcile(cfg *config.Config) {
	configured := make(map[accountIdentity]struct{}, len(cfg.Providers))
	for i := range cfg.Providers {
		configured[identifyAccount(&cfg.Providers[i])] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configured = configured
	for key, load := range s.loads {
		if _, ok := configured[key]; !ok && load.active == 0 {
			delete(s.loads, key)
		}
	}
}

// Selection and reservation share one critical section, so concurrent arrivals
// see the reservation before choosing their own account. Eligibility consumes a
// rate-limit token only for the selected candidate, as in the existing router.
func (s *accountScheduler) selectAndStart(ctx context.Context, candidates []accountCandidate, eligible func(string) error) (int, *accountLease, []accountSkip) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var skipped []accountSkip
	for ctx.Err() == nil {
		best := -1
		var bestLoad *accountLoad
		for i := range candidates {
			if candidates[i].tried {
				continue
			}
			load := s.loadLocked(candidates[i].key)
			if best < 0 || load.active < bestLoad.active ||
				(load.active == bestLoad.active && (load.lastAttempt.Before(bestLoad.lastAttempt) ||
					(load.lastAttempt.Equal(bestLoad.lastAttempt) && load.sequence < bestLoad.sequence))) {
				best, bestLoad = i, load
			}
		}
		if best < 0 {
			break
		}
		if err := eligible(candidates[best].provider.Name); err != nil {
			candidates[best].tried = true
			skipped = append(skipped, accountSkip{index: best, err: err})
			continue
		}
		return best, s.startLocked(candidates[best].key, bestLoad), skipped
	}
	return -1, nil, skipped
}

func (s *accountScheduler) loadLocked(key accountIdentity) *accountLoad {
	load := s.loads[key]
	if load == nil {
		load = &accountLoad{}
		s.loads[key] = load
	}
	return load
}

func (s *accountScheduler) start(key accountIdentity) *accountLease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked(key, s.loadLocked(key))
}

func (s *accountScheduler) startLocked(key accountIdentity, load *accountLoad) *accountLease {
	previous := load.lastAttempt
	s.sequence++
	load.active++
	load.lastAttempt = time.Now()
	load.sequence = s.sequence
	return &accountLease{scheduler: s, key: key, load: load, active: load.active, previousAttempt: previous}
}

type accountLease struct {
	scheduler       *accountScheduler
	key             accountIdentity
	load            *accountLoad
	active          int
	previousAttempt time.Time
	once            sync.Once
}

func (l *accountLease) finish() {
	l.once.Do(func() {
		s := l.scheduler
		s.mu.Lock()
		defer s.mu.Unlock()
		l.load.active--
		if _, configured := s.configured[l.key]; !configured && l.load.active == 0 {
			delete(s.loads, l.key)
		}
	})
}
