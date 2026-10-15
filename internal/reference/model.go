package reference

import (
	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/sim"
)

// Load mix the load generator sends, by GraphQL operation.
var Ops = []sim.Op{
	{Name: "Browse", Service: Gateway, Weight: 0.5},
	{Name: "AccountPage", Service: Gateway, Weight: 0.3},
	{Name: "Checkout", Service: Gateway, Weight: 0.2},
}

// SimModel describes the reference system from its source code rather
// than from observation: which calls each operation makes, and the
// synthetic service times. The control plane replaces it with a model
// calibrated from live traffic once there is some.
func SimModel(sys backdraft.SystemConfig) *sim.Model {
	one := func(peer, method string) []sim.Call { return []sim.Call{{Peer: peer, Method: method}} }
	shapes := map[string]map[string][]sim.Shape{
		Gateway: {
			"Browse":      {{one(Catalog, "GetProducts")}},
			"AccountPage": {{one(Account, "GetAccount"), one(Order, "GetOrdersForAccount")}},
			"Checkout":    {{one(Order, "PostOrder")}},
		},
		Order: {
			"PostOrder": {{one(Account, "GetAccount"), one(Catalog, "GetProducts")}},
		},
	}
	m := &sim.Model{Entry: sys.Entry, Ops: Ops, Services: map[string]sim.ServiceModel{}}
	for name, cfg := range sys.Services {
		m.Services[name] = sim.ServiceModel{
			Config:     cfg.Clone(),
			MeanSelfMs: cfg.Server.WorkMs,
			Shapes:     shapes[name],
		}
	}
	return m
}
