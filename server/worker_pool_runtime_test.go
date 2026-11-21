package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestPausedTaskResumeReloadsGlobalWorkersAndTaskOverride(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatalf("test database unavailable: %v", err)
	}
	defer m.Close()
	oldWorkers, oldPools := m.Workers(), m.WorkerPools()
	defer m.SetWorkerPools(oldPools, oldWorkers)
	if err := m.SetWorkerPools(nil, 2); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("worker resume regression", "no external actions", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := NewEngine(m) // no resolver: no model calls or tools can execute
	defer func() { e.StopTask(task.ID); _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	e.Run(ctx, task)
	count := func(want int) {
		t.Helper()
		waitWorkerCondition(t, func() bool { actual, target := e.WorkerCounts(task); return actual == want && target == want })
	}
	count(2)
	if err := m.SetWorkers(1); err != nil {
		t.Fatal(err)
	}
	e.Run(ctx, task) // editing a running task must not resize it immediately
	count(2)
	e.Pause(task.ID, agent.AbortPausedByUser)
	e.Run(ctx, task)
	e.Resume(task)
	count(1)
	override := 3
	if _, err := m.UpdateTaskMetadata(task.ID, db.TaskPatch{WorkerCount: &override}); err != nil {
		t.Fatal(err)
	}
	count(1)
	e.Pause(task.ID, agent.AbortPausedByUser)
	e.Run(ctx, task)
	e.Resume(task)
	count(3)
	if taskDTO(task, "running").WorkerCount != 3 {
		t.Fatal("override missing from DTO")
	}
	override = 0
	if _, err := m.UpdateTaskMetadata(task.ID, db.TaskPatch{WorkerCount: &override}); err != nil {
		t.Fatal(err)
	}
	e.Pause(task.ID, agent.AbortPausedByUser)
	e.Run(ctx, task)
	e.Resume(task)
	count(1)
}
