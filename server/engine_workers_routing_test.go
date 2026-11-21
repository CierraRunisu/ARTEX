package server

import (
	"context"
	"testing"
)

// A blocked generation models an Execute plus its final writes. No database,
// provider, or task store is involved; replacements have no resolver and idle.
func TestUnifiedWorkerPoolDrainsRoutingGeneration(t *testing.T) {
	e := NewEngine(nil)
	task := &Task{ID: "pure-routing"}
	rt := e.registerTaskRoutines(context.Background(), task.ID, 1)
	oldCtx, retire := context.WithCancel(rt.ctx)
	oldSlot := WorkerSlot{Name: "work#1", ProfileID: 11, TaskDefault: true}
	pool := &taskWorkerPool{runtime: rt, task: task, loops: map[string]*workerSlotRuntime{}}
	old := &workerSlotRuntime{pool: pool, slot: oldSlot, idleCtx: oldCtx, retire: retire, done: make(chan struct{})}
	pool.loops[oldSlot.Name] = old
	e.workerPools.Store(task.ID, pool)
	e.slotPlans.Store(task.ID, map[string]WorkerSlot{oldSlot.Name: oldSlot})
	defer e.StopTask(task.ID)
	apply := func(slots ...WorkerSlot) {
		e.deleteMu.RLock()
		pool.mu.Lock()
		e.reconcileWorkerSlotsLocked(pool, slots)
		pool.mu.Unlock()
		e.deleteMu.RUnlock()
	}
	next := WorkerSlot{Name: "work#1", TaskDefault: true}
	apply(next)
	if oldCtx.Err() == nil || rt.ctx.Err() != nil {
		t.Fatal("retirement must stop claims without cancelling Execute")
	}
	s := &Server{engine: e}
	plan, applied, err := s.workerResolutionSlots(task)
	if err != nil || !applied || plan[oldSlot.Name] != oldSlot {
		t.Fatalf("draining route lost: %v %v %v", plan, applied, err)
	}
	if actual, target := e.WorkerCounts(task); actual != 1 || target != 1 {
		t.Fatalf("replacement overlaps: %d/%d", actual, target)
	}
	// Rapid changes coalesce to the latest desired model before the old name exits.
	apply(WorkerSlot{Name: "work#1", ProfileID: 23, TaskDefault: true})
	apply(next)
	e.finishWorkerSlot(old)
	rt.wg.Done()
	pool.mu.Lock()
	replacement := pool.loops[next.Name]
	if replacement == old || replacement.slot != next {
		t.Fatal("latest generation was not installed")
	}
	pool.mu.Unlock()
	plan, _, _ = s.workerResolutionSlots(task)
	if plan[next.Name] != next {
		t.Fatalf("TaskDefault lost on replacement: %v", plan)
	}
	// Shrinking retains continuation routing but drops the retired live label.
	apply()
	<-replacement.done
	plan, _, _ = s.workerResolutionSlots(task)
	if len(plan) != 0 {
		t.Fatalf("retired route reported as live: %v", plan)
	}
	historical, _ := e.slotPlans.Load(task.ID)
	if historical.(map[string]WorkerSlot)[next.Name] != next {
		t.Fatal("continuation route lost")
	}
}
