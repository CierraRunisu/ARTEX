package server

import (
	"github.com/Autumn-27/artex/db"
	"testing"
)

func TestWorkerLLMResolutionAppliedGeneration(t *testing.T) {
	m := &Manager{}
	task := &Task{ID: "1", WorkerCount: 2, WorkerLLMProfileIDs: []int64{23, 0}}
	s := &Server{m: m, engine: &Engine{}}
	appliedPlan := map[string]WorkerSlot{
		"work#1": {Name: "work#1", ProfileID: 11, TaskDefault: true},
		"work#2": {Name: "work#2", TaskDefault: true},
	}
	s.engine.slotPlans.Store(task.ID, appliedPlan)
	pool := &taskWorkerPool{task: task, loops: map[string]*workerSlotRuntime{}}
	for name, slot := range appliedPlan {
		pool.loops[name] = &workerSlotRuntime{slot: slot}
	}
	s.engine.workerPools.Store(task.ID, pool)
	plan, applied, err := s.workerResolutionSlots(task)
	if err != nil || !applied || plan["work#1"].ProfileID != 11 {
		t.Fatalf("saved next-generation assignment replaced applied plan: %+v %v %v", plan, applied, err)
	}
	s.engine.workerPools.Delete(task.ID)
	plan, applied, err = s.workerResolutionSlots(task)
	if err != nil || applied || plan["work#1"].ProfileID != 23 || !plan["work#2"].TaskDefault {
		t.Fatalf("next-start preview: %+v %v %v", plan, applied, err)
	}
}

func TestWorkerLLMResolutionOwnerDTO(t *testing.T) {
	got := taskNodeDTO(&db.Node{ID: 987, Owner: "work#2"})
	if got.ID != "987" || got.Owner != "work#2" {
		t.Fatalf("owner lost: %+v", got)
	}
}

func TestWorkerLLMResolutionRouting(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	ids := make([]int64, 3)
	for i, model := range []string{"slot-model", "task-model", "bound-model"} {
		ids[i], err = m.pg.SaveProfile(&db.LLMProfile{Name: model, Format: "openai", Model: model, APIKey: "test-key"})
		if err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, id := range ids {
			_ = m.pg.DeleteProfile(id)
		}
	}()
	task, err := m.CreateTaskWithOptions("worker resolution", "labels", db.TaskCreateOptions{LLMProfileIDs: []int64{ids[1]}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	old, err := m.pg.GetAgentByKey("worker")
	if err != nil {
		t.Fatal(err)
	}
	var previous *int64
	if old != nil {
		previous = old.LLMProfileID
	}
	defer m.pg.SetAgentLLMProfile("worker", previous)
	if err := m.pg.SetAgentLLMProfile("worker", &ids[2]); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, provByProfile: map[int64]*provEntry{}}
	for _, tc := range []struct {
		slot          WorkerSlot
		model, source string
	}{
		{WorkerSlot{ProfileID: ids[0], TaskDefault: true}, "slot-model", "worker_assignment"},
		{WorkerSlot{TaskDefault: true}, "task-model", "task_chain"},
		{WorkerSlot{}, "bound-model", "agent_binding"},
	} {
		got, err := s.resolveWorkerSlotLLM(task, tc.slot)
		if err != nil || !got.Available || got.Model != tc.model || got.Source != tc.source {
			t.Fatalf("slot %+v: %+v, %v", tc.slot, got, err)
		}
	}
	missing := int64(999999999)
	got, err := s.resolveWorkerSlotLLM(task, WorkerSlot{ProfileID: missing})
	if err != nil || got.Available || got.ProfileID == nil || *got.ProfileID != missing || got.Source != "worker_assignment" {
		t.Fatalf("missing assignment fell back: %+v %v", got, err)
	}
	// A cleared task chain and an unbound dedicated slot follow global resolution,
	// bypassing the legacy role binding even when that binding is usable.
	task.setLLMState(nil, nil, nil, task.llmStateSnapshot().ChainRevision, "", "")
	got, err = s.resolveWorkerSlotLLM(task, WorkerSlot{TaskDefault: true})
	if err != nil || got.Source == "agent_binding" || got.Source == "task_chain" {
		t.Fatalf("dedicated default did not reach global: %+v %v", got, err)
	}
}
