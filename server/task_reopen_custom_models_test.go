package server

import (
	"fmt"
	"slices"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestReopenPreservesCustomModelAssignments(t *testing.T) {
	requirePlannerTestDB(t)
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var ids []int64
	for i := 0; i < 2; i++ {
		id, err := m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("reopen-custom-%d", i), Format: "openai", Model: fmt.Sprintf("local-%d", i), BaseURL: "http://127.0.0.1:1/v1"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		defer m.pg.DeleteProfile(id)
	}
	task, err := m.CreateTaskWithOptions("reopen custom routing", "local test only", db.TaskCreateOptions{PlannerLLMProfileID: ids[0], WorkerLLMProfileIDs: []int64{ids[1], 0}, WorkerCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	if _, err = task.Store.AddIntent(map[string]any{"summary": "remaining local work"}, 6, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	if err = m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	res, err := s.resumeOrReopenTask(task, agent.AbortPausedByOrchestrator)
	if err != nil || res.Status != "running" {
		t.Fatalf("reopen: %+v %v", res, err)
	}
	stored, err := m.pg.GetTask(taskIDInt(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored.PlannerLLMProfileID != ids[0] || !slices.Equal(stored.WorkerLLMProfileIDs, []int64{ids[1], 0}) || stored.WorkerCount != 2 {
		t.Fatalf("reopen lost model assignments: %+v", taskDTO(task, "running"))
	}
	// Verify the same dynamic Planner provider still selects the explicit task model.
	provider := &scriptedLLMProvider{}
	s.provByProfile = map[int64]*provEntry{ids[0]: {prov: provider, cfg: agent.Config{Model: "local-0"}}}
	runtime := &taskLLMRuntime{s: s, taskID: task.ID, agentKey: "planner"}
	selected, err := runtime.current()
	if err != nil || selected.provider != provider {
		t.Fatalf("Planner routing after reopen: %+v %v", selected, err)
	}
	slots, err := workerSlotsForTask(task, nil, 5)
	if err != nil || len(slots) != 2 || slots[0].ProfileID != ids[1] || !slots[1].TaskDefault {
		t.Fatalf("Worker routing after reopen: %+v %v", slots, err)
	}
}
