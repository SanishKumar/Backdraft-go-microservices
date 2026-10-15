package backdraft

import (
	"sync"
	"time"
)

const budgetWindow = 10 // seconds

// Budget is a retry budget in the style of Finagle and Linkerd: over a
// sliding window, retries may not exceed Ratio of requests plus a small
// floor so low-traffic clients can still retry. Unlike a per-request retry
// count, it bounds amplification during an outage to 1+Ratio per hop no
// matter how many layers retry.
type Budget struct {
	mu        sync.Mutex
	ratio     float64
	minPerSec float64
	now       func() time.Time
	buckets   [budgetWindow]budgetBucket
}

type budgetBucket struct {
	sec               int64
	requests, retries float64
}

func NewBudget(p BudgetPolicy) *Budget {
	return &Budget{ratio: p.Ratio, minPerSec: p.MinPerSec, now: time.Now}
}

// Set updates the policy in place, keeping the window's history.
func (b *Budget) Set(p BudgetPolicy) {
	b.mu.Lock()
	b.ratio, b.minPerSec = p.Ratio, p.MinPerSec
	b.mu.Unlock()
}

func (b *Budget) bucket(sec int64) *budgetBucket {
	bk := &b.buckets[sec%budgetWindow]
	if bk.sec != sec {
		*bk = budgetBucket{sec: sec}
	}
	return bk
}

// RecordRequest counts a first attempt, which earns Ratio retries.
func (b *Budget) RecordRequest() {
	b.mu.Lock()
	b.bucket(b.now().Unix()).requests++
	b.mu.Unlock()
}

// AllowRetry reports whether a retry fits in the budget and, if so,
// charges it.
func (b *Budget) AllowRetry() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().Unix()
	var requests, retries float64
	for _, bk := range b.buckets {
		if now-bk.sec < budgetWindow {
			requests += bk.requests
			retries += bk.retries
		}
	}
	if retries+1 > b.ratio*requests+b.minPerSec*budgetWindow {
		return false
	}
	b.bucket(now).retries++
	return true
}
