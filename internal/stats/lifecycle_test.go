package stats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchivesPreserveCountersAndLateOutcomes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".stats.json")
	c := New(path)
	old := ProviderIdentity{"old", "model-old", 1}
	current := ProviderIdentity{"current", "model-new", 2}
	c.ReconcileProviders([]ProviderIdentity{old})
	c.Record("1", old.Name, old.ModelID, old.Priority, true, 200, "", 10*time.Millisecond)
	c.ReconcileProviders([]ProviderIdentity{current})
	if len(c.providers) != 0 || len(c.modelLatency) != 0 || len(c.prioSeries) != 0 {
		t.Fatal("inactive history remains resident")
	}
	c.Record("2", old.Name, old.ModelID, old.Priority, true, 200, "", 20*time.Millisecond)
	c.Save()
	c = New(path)
	c.ReconcileProviders([]ProviderIdentity{old})
	snap := c.Snapshot()
	if snap.TotalReq != 2 || len(snap.Providers) != 1 || snap.Providers[0].Total != 2 || snap.Providers[0].LatencySumMs != 30 {
		t.Fatalf("lost history: %+v", snap)
	}
	archive, _ := os.ReadFile(c.archivePath(old))
	c.Reset()
	_ = os.WriteFile(c.archivePath(old), archive, 0600)
	c.ReconcileProviders([]ProviderIdentity{current})
	c.Save()
	c = New(path)
	c.ReconcileProviders([]ProviderIdentity{old})
	if snap = c.Snapshot(); snap.TotalReq != 0 || (len(snap.Providers) > 0 && snap.Providers[0].Total != 0) {
		t.Fatal("reset resurrected archived counters")
	}
}

func TestAutoSaveCanBeJoined(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	c := New(path)
	stop := make(chan struct{})
	done := c.StartAutoSave(stop, time.Hour)
	c.RecordClientOutcome(c.RecordClientRequest("x"), true)
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("autosave did not exit")
	}
	if New(path).Snapshot().TotalClientSuccess != 1 {
		t.Fatal("final outcome not persisted")
	}
}
