package stats

import (
	"testing"
	"time"
)

func TestRateHistoryKeepsBoundedPredecessorsWithoutGrowingDisplayedOrLatencySeries(t *testing.T) {
	c := New("")
	const attempts = 700
	for i := 0; i < attempts; i++ {
		c.Record("request", "account", "model", 1, i%3 != 0, 200, "", time.Millisecond)
	}
	snapshot := c.Snapshot()
	for _, curve := range snapshot.Curves {
		if len(curve.Points) != hitSeriesCap || len(curve.Successes) != hitSeriesCap || len(curve.Requests) != hitSeriesCap || len(curve.History) != 99 {
			t.Fatalf("curve %d has invalid visible/history bounds: %+v", curve.Priority, curve)
		}
		for i, success := range curve.History {
			if success != ((attempts-hitRateSeriesCap+i)%3 != 0) {
				t.Fatalf("curve %d history order changed at %d", curve.Priority, i)
			}
		}
		for i, success := range curve.Successes {
			if success != ((attempts-hitSeriesCap+i)%3 != 0) {
				t.Fatalf("curve %d visible order changed at %d", curve.Priority, i)
			}
			var successes int
			for j := attempts - hitSeriesCap + i - hitWindowSize + 1; j <= attempts-hitSeriesCap+i; j++ {
				if j%3 != 0 {
					successes++
				}
			}
			if curve.Points[i] != float64(successes)/hitWindowSize*100 {
				t.Fatalf("server rolling rate changed at %d", i)
			}
		}
	}
	if len(c.overallSeries) != hitRateSeriesCap || len(c.prioSeries[1]) != hitRateSeriesCap || len(snapshot.LatencyCurve) != hitSeriesCap {
		t.Fatal("rate or latency retention limit changed")
	}
	snapshot.Curves[0].History[0] = !snapshot.Curves[0].History[0]
	if snapshot.Curves[0].History[0] == c.Snapshot().Curves[0].History[0] {
		t.Fatal("snapshot history aliases collector state")
	}
	c.Reset()
	if curve := c.Snapshot().Curves[0]; len(curve.History)+len(curve.Points)+len(curve.Successes) != 0 {
		t.Fatal("reset retained rolling prehistory")
	}
}

func TestRateHistoryPreservesWarmupAndDoesNotIncludeCancellations(t *testing.T) {
	c := New("")
	for i := 0; i < 250; i++ {
		c.Record("request", "a", "model-a", 1, i%2 == 0, 200, "", time.Millisecond)
	}
	c.Record("request", "b", "model-b", 2, false, 429, "limit", time.Millisecond)
	c.RecordCanceled("request", "b", "model-b", 2, "client left", time.Millisecond)
	snapshot := c.Snapshot()
	if len(snapshot.Curves[0].History) != 11 || len(snapshot.Curves[1].History) != 10 {
		t.Fatal("each curve must retain only its own predecessors")
	}
	p2 := snapshot.Curves[2]
	if len(p2.History) != 0 || len(p2.Successes) != 1 || p2.Successes[0] || p2.Points[0] != 0 {
		t.Fatalf("cancellation or other priorities polluted P2: %+v", p2)
	}
}
