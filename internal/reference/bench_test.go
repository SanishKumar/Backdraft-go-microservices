package reference

import (
	"testing"

	"github.com/SanishKumar/go-microservices/backdraft/sim"
)

func BenchmarkMetastableBrownout(b *testing.B) {
	m := SimModel(System())
	sc := sim.Brownout(250, Catalog, 300, 6, 8, 25, 1)
	b.ReportAllocs()
	var events int
	for i := 0; i < b.N; i++ {
		events = sim.Run(m, sc).Events
	}
	b.ReportMetric(float64(events), "events/op")
}

func BenchmarkStabilityMap(b *testing.B) {
	m := SimModel(System())
	for i := 0; i < b.N; i++ {
		sim.StabilityMap(m, sim.MapSpec{Service: Catalog, LatencyMs: 300,
			Loads:     []float64{44, 89, 133, 178, 222, 266, 311, 355, 400, 444},
			Durations: []float64{1, 2, 4, 6, 8, 12, 16, 20}})
	}
}
