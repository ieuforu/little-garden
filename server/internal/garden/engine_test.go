package garden

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T) (*Store, *Engine, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "garden.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Shutdown(); store.Close() })
	return store, engine, path
}
func sample(id string) PlantRequest {
	return PlantRequest{GardenID: 1, RequestID: id, X: 1100, Y: 950, Palette: 1}
}

func TestConcurrentRetryIsCommittedAndBroadcastOnce(t *testing.T) {
	_, engine, path := setup(t)
	sub := engine.Subscribe()
	<-sub.Events
	var wg sync.WaitGroup
	results := make(chan PlantResult, 20)
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := engine.Plant(context.Background(), "visitor", sample("request-1234"))
			if err != nil {
				failures <- err
			} else {
				results <- result
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
	}
	var id string
	count := 0
	for result := range results {
		if id == "" {
			id = result.Flower.ID
		}
		if id != result.Flower.ID {
			t.Fatal("duplicate flowers")
		}
		if !result.Duplicate {
			count++
		}
	}
	if count != 1 || len(engine.Snapshot().Flowers) != 17 {
		t.Fatalf("commits=%d flowers=%d", count, len(engine.Snapshot().Flowers))
	}
	select {
	case data := <-sub.Events:
		var event PlantResult
		if json.Unmarshal(data, &event) != nil || event.Flower.ID != id {
			t.Fatal("wrong broadcast")
		}
	default:
		t.Fatal("missing broadcast")
	}
	select {
	case <-sub.Events:
		t.Fatal("duplicate broadcast")
	default:
	}
	// Read from an independent database handle, simulating the next process.
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := New(context.Background(), reopened)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Snapshot().Garden.Revision != 1 || len(recovered.Snapshot().Flowers) != 17 {
		t.Fatal("committed state lost")
	}
}

func TestSnapshotAndSubscriptionHaveNoGap(t *testing.T) {
	_, e, _ := setup(t)
	for i := 0; i < 20; i++ {
		if err := e.NewGarden(context.Background()); err != nil {
			t.Fatal(err)
		}
		req := sample(fmt.Sprintf("subscribe-%d", i))
		req.GardenID = e.Snapshot().Garden.ID
		start := make(chan struct{})
		done := make(chan error, 1)
		go func() { <-start; _, err := e.Plant(context.Background(), "visitor", req); done <- err }()
		close(start)
		sub := e.Subscribe()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		var snap Snapshot
		if err := json.Unmarshal(<-sub.Events, &snap); err != nil {
			t.Fatal(err)
		}
		if snap.Garden.Revision == 0 {
			select {
			case data := <-sub.Events:
				var event PlantResult
				_ = json.Unmarshal(data, &event)
				if event.Revision != 1 {
					t.Fatal("missed event")
				}
			case <-time.After(time.Second):
				t.Fatal("snapshot/subscription race")
			}
		} else if len(snap.Flowers) != 1 {
			t.Fatal("inconsistent snapshot")
		}
		e.Unsubscribe(sub)
	}
}

func TestValidationModerationAndBackpressure(t *testing.T) {
	_, e, _ := setup(t)
	ctx := context.Background()
	for _, bad := range []PlantRequest{{GardenID: 1, RequestID: "valid-id", X: math.NaN(), Y: 200}, {GardenID: 1, RequestID: "valid-id", X: 400, Y: 1500}, {GardenID: 1, RequestID: "valid-id", X: 900, Y: 900, Palette: 4}} {
		if _, err := e.Plant(ctx, "visitor", bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid: %v", err)
		}
	}
	result, err := e.Plant(ctx, "visitor", sample("request-1234"))
	if err != nil {
		t.Fatal(err)
	}
	changed := sample("request-1234")
	changed.Palette = 3
	if _, err = e.Plant(ctx, "visitor", changed); !errors.Is(err, ErrConflict) {
		t.Fatal("request ID reused for different data")
	}
	near := sample("request-near")
	near.X++
	if _, err = e.Plant(ctx, "other", near); !errors.Is(err, ErrSpacing) {
		t.Fatal("overlap accepted")
	}
	if err = e.Pause(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Plant(ctx, "other", sample("request-paused")); !errors.Is(err, ErrPaused) {
		t.Fatal("pause bypassed")
	}
	if err = e.BanFlower(ctx, result.Flower.ID); err != nil {
		t.Fatal(err)
	}
	if !e.Banned("visitor") || len(e.Snapshot().Flowers) != 16 || e.Snapshot().Garden.Planted != 17 {
		t.Fatal("moderation did not preserve write budget")
	}
	if _, err = e.Plant(ctx, "visitor", sample("request-1234")); !errors.Is(err, ErrBanned) {
		t.Fatal("ban bypassed by idempotency")
	}
	// A slow subscriber is detached when its bounded queue fills.
	sub := e.Subscribe()
	for i := 0; i < 40; i++ {
		if err = e.Pause(ctx, i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}
	e.mu.Lock()
	_, present := e.subs[sub]
	e.mu.Unlock()
	if present {
		t.Fatal("slow subscriber retained")
	}
	for range sub.Events {
	} // Must terminate because the detached channel was closed.
}

func TestRollbackOnFailedCommitAndBackupRestore(t *testing.T) {
	store, e, path := setup(t)
	ctx := context.Background()
	sub := e.Subscribe()
	<-sub.Events
	_, err := store.db.Exec("CREATE TRIGGER reject_plant BEFORE INSERT ON flowers BEGIN SELECT RAISE(ABORT,'simulated disk error'); END")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Plant(ctx, "visitor", sample("request-fail")); err == nil {
		t.Fatal("expected storage failure")
	}
	if len(e.Snapshot().Flowers) != 16 || e.Snapshot().Garden.Revision != 0 {
		t.Fatal("uncommitted state became visible")
	}
	select {
	case <-sub.Events:
		t.Fatal("uncommitted broadcast")
	default:
	}
	_, _ = store.db.Exec("DROP TRIGGER reject_plant")
	if _, err = e.Plant(ctx, "visitor", sample("request-good")); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err = Backup(ctx, path, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	recovered, err := New(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Snapshot().Flowers) != 17 || recovered.Snapshot().Garden.Revision != 1 {
		t.Fatal("backup missed WAL writes")
	}
	if err = Backup(ctx, path, backup); err == nil {
		t.Fatal("overwrote an existing backup")
	}
}
