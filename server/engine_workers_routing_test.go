package server

import (
	"context"
	"github.com/Autumn-27/artex/agent"
	"testing"
)

// A blocked generation models an Execute plus its final writes. No database,
// provider, or task store is involved; replacements stay paused and idle.
func TestUnifiedWorkerPoolDrainsRoutingGeneration(t *testing.T) {
	e := NewEngine(nil)
	// Paused inert workers resolve successfully, then wait without touching storage.
	e.paused.Store("pure-routing", true)
	e.workerResolve = func(*Task, WorkerSlot) (*agent.Worker, error) { return &agent.Worker{}, nil }
	task := &Task{ID: "pure-routing", WorkerCount: 1, WorkerLLMProfileIDs: []int64{0}}
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
	slots, err := workerSlotsForTask(task, []WorkerPool{{ProfileID: 99, Workers: 4}}, 4)
	if err != nil || len(slots) != 1 {
		t.Fatalf("task override slots: %v %v", slots, err)
	}
	next := slots[0]
	if next.ProfileID != 0 || !next.TaskDefault {
		t.Fatalf("zero assignment inherited global pool: %v", next)
	}
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
	pool.mu.Lock()
	if pool.desired[next.Name] != next || pool.loops[next.Name] != old {
		t.Fatal("drain did not separate actual and target models")
	}
	pool.mu.Unlock()
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
	// Expanding explicit [0] applies task-default routing to every new ordinal,
	// even though the configured global pool pins a completely different model.
	task.updateLifecycle(func(state *taskLifecycleState) { state.WorkerCount = 3 })
	expanded, err := workerSlotsForTask(task, []WorkerPool{{ProfileID: 99, Workers: 4}}, 4)
	if err != nil {
		t.Fatal(err)
	}
	apply(expanded...)
	plan, _, _ = s.workerResolutionSlots(task)
	if len(plan) != 3 {
		t.Fatalf("expansion plan: %v", plan)
	}
	for _, slot := range plan {
		if slot.ProfileID != 0 || !slot.TaskDefault {
			t.Fatalf("expansion lost task default: %v", slot)
		}
	}
	pool.mu.Lock()
	second, third := pool.loops["work#2"], pool.loops["work#3"]
	if pool.loops[next.Name] != replacement {
		t.Fatal("expansion replaced unchanged live generation")
	}
	pool.mu.Unlock()
	apply(next)
	<-second.done
	<-third.done
	if actual, target := e.WorkerCounts(task); actual != 1 || target != 1 {
		t.Fatalf("shrink did not converge: %d/%d", actual, target)
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
