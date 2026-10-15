// Package reference holds the resilience policies the reference system
// ships with.
//
// They are deliberately ordinary. Every hop was configured the way a team
// configures its own client in isolation: three attempts, a timeout that
// looks generous next to normal latency, retry on UNAVAILABLE and
// DEADLINE_EXCEEDED. No single line is wrong. Backdraft's job is to show
// what they add up to.
package reference

import "github.com/SanishKumar/go-microservices/backdraft"

// Service names, as they appear in policies and spans.
const (
	Users   = "users"
	Gateway = "gateway"
	Account = "account"
	Catalog = "catalog"
	Order   = "order"
)

var retryTransient = []string{"UNAVAILABLE", "DEADLINE_EXCEEDED"}

func threeTries(timeoutMs int, propagate bool) backdraft.ClientPolicy {
	return backdraft.ClientPolicy{
		TimeoutMs:         timeoutMs,
		MaxAttempts:       3,
		BackoffMs:         25,
		BackoffMaxMs:      200,
		RetryOn:           retryTransient,
		PropagateDeadline: propagate,
	}
}

// UsersPolicy models people: they wait about two seconds, then hit
// refresh once.
func UsersPolicy() backdraft.ServiceConfig {
	return backdraft.ServiceConfig{Clients: map[string]backdraft.ClientPolicy{
		Gateway: {
			TimeoutMs:         2000,
			MaxAttempts:       2,
			BackoffMs:         250,
			BackoffMaxMs:      250,
			RetryOn:           []string{"UNAVAILABLE", "DEADLINE_EXCEEDED", "RESOURCE_EXHAUSTED", "INTERNAL"},
			PropagateDeadline: true,
		},
	}}
}

func GatewayPolicy() backdraft.ServiceConfig {
	return backdraft.ServiceConfig{
		Server: backdraft.ServerPolicy{WorkMs: 0.5},
		Clients: map[string]backdraft.ClientPolicy{
			Account: threeTries(600, true),
			Catalog: threeTries(600, true),
			Order:   threeTries(1500, true),
		},
		Methods: map[string]backdraft.MethodInfo{
			"Browse":      {Idempotent: true},
			"AccountPage": {Idempotent: true},
			"Checkout":    {Dedupes: true},
		},
	}
}

func AccountPolicy() backdraft.ServiceConfig {
	return backdraft.ServiceConfig{
		Server: backdraft.ServerPolicy{Workers: 16, WorkMs: 3},
		Methods: map[string]backdraft.MethodInfo{
			"PostAccount": {Idempotent: false},
			"GetAccount":  {Idempotent: true},
			"GetAccounts": {Idempotent: true},
		},
	}
}

func CatalogPolicy() backdraft.ServiceConfig {
	return backdraft.ServiceConfig{
		Server: backdraft.ServerPolicy{Workers: 8, WorkMs: 12},
		Methods: map[string]backdraft.MethodInfo{
			"PostProduct": {Idempotent: false},
			"GetProduct":  {Idempotent: true},
			"GetProducts": {Idempotent: true},
		},
	}
}

// OrderPolicy: order builds its downstream calls from a fresh context,
// a common way deadline propagation gets lost in practice, and copied the
// gateway's 600ms timeout.
func OrderPolicy() backdraft.ServiceConfig {
	return backdraft.ServiceConfig{
		Server: backdraft.ServerPolicy{Workers: 16, WorkMs: 4},
		Clients: map[string]backdraft.ClientPolicy{
			Account: threeTries(600, false),
			Catalog: threeTries(600, false),
		},
		Methods: map[string]backdraft.MethodInfo{
			"PostOrder":           {Dedupes: true},
			"GetOrdersForAccount": {Idempotent: true},
		},
	}
}

// System returns the whole reference system's declared policy.
func System() backdraft.SystemConfig {
	return backdraft.SystemConfig{
		Entry: Users,
		Services: map[string]backdraft.ServiceConfig{
			Users:   UsersPolicy(),
			Gateway: GatewayPolicy(),
			Account: AccountPolicy(),
			Catalog: CatalogPolicy(),
			Order:   OrderPolicy(),
		},
	}
}
