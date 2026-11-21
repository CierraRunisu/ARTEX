package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestTaskWorkerModelsPersistenceValidationAndRouting(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("worker-assignment-%d", i), Format: "openai", Model: fmt.Sprintf("model-%d", i), APIKey: "test-secret-not-returned", BaseURL: "http://127.0.0.1:1/v1"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		defer m.pg.DeleteProfile(id)
	}
	task, err := m.CreateTask("worker assignment", "local configuration test only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	if err := m.ApplyTaskPause(task.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	defer func() { cancel(); s.Close() }()
	token, _ := signJWT(s.jwtKey)
	patch := func(body string, want int) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s: status=%d body=%s", body, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	body := patch(fmt.Sprintf(`{"worker_count":3,"worker_llm_profile_ids":[%d,0,%d]}`, ids[0], ids[1]), 200)
	if strings.Contains(body, "test-secret") {
		t.Fatal("credentials leaked")
	}
	var dto TaskDTO
	if err := json.Unmarshal([]byte(body), &dto); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dto.WorkerLLMProfileIDs, []int64{ids[0], 0, ids[1]}) {
		t.Fatalf("bad DTO: %v", dto.WorkerLLMProfileIDs)
	}
	stored, err := m.pg.GetTask(taskIDInt(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored.WorkerLLMProfileIDs, dto.WorkerLLMProfileIDs) || !stored.Paused {
		t.Fatal("assignment not persisted or task resumed")
	}
	reloaded := taskFromPG(stored, nil, nil)
	if !slices.Equal(reloaded.lifecycleSnapshot().WorkerLLMProfileIDs, dto.WorkerLLMProfileIDs) {
		t.Fatal("reload lost Worker assignments")
	}
	for _, body := range []string{`{"worker_llm_profile_ids":[-1]}`, `{"worker_llm_profile_ids":[9223372036854775807]}`, `{"worker_llm_profile_ids":[1.5]}`, `{"worker_llm_profile_ids":"bad"}`, `{"worker_llm_profile_ids":[` + strings.Repeat("0,", 50) + `0]}`} {
		patch(body, 400)
	}
	if !slices.Equal(task.lifecycleSnapshot().WorkerLLMProfileIDs, dto.WorkerLLMProfileIDs) {
		t.Fatal("invalid edit changed configuration")
	}
	if _, err := m.ReplaceTaskLLMProfiles(task.ID, []int64{ids[2]}, ids[2]); err != nil {
		t.Fatal(err)
	}
	old, err := m.pg.GetAgentByKey("worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetAgentLLMProfile("worker", &ids[0]); err != nil {
		t.Fatal(err)
	}
	defer m.pg.SetAgentLLMProfile("worker", old.LLMProfileID)
	runtime := &taskLLMRuntime{s: s, taskID: task.ID, agentKey: "worker", taskDefault: true}
	selected, err := runtime.current()
	if err != nil || selected.profileID != ids[2] {
		t.Fatalf("default did not follow task: %+v %v", selected, err)
	}
	cfg, ok := runtime.activeCfg()
	if !ok || cfg.Model != "model-2" {
		t.Fatalf("runtime settings used role binding: %+v", cfg)
	}
	slots, err := workerSlotsForTask(task, []WorkerPool{{ProfileID: ids[2], Workers: 5}}, 5)
	if err != nil || len(slots) != 3 || slots[0].ProfileID != ids[0] || slots[1].ProfileID != 0 || !slots[1].TaskDefault || slots[2].ProfileID != ids[1] {
		t.Fatalf("bad slots: %+v %v", slots, err)
	}
	pinned := &taskLLMRuntime{s: s, taskID: task.ID, agentKey: "worker", pinProfileID: &ids[0]}
	cfg, ok = pinned.activeCfg()
	if !ok || cfg.Model != "model-0" {
		t.Fatal("pinned Worker ignored assignment")
	}
	if err := m.pg.DeleteProfile(ids[0]); err != nil {
		t.Fatal(err)
	}
	s.invalidateProfileAgents() // profile deletion API invalidates provider caches
	if _, err := pinned.current(); err == nil {
		t.Fatal("deleted pinned model silently fell back")
	}
	patch(`{"worker_llm_profile_ids":[]}`, 200)
	if len(task.lifecycleSnapshot().WorkerLLMProfileIDs) != 0 {
		t.Fatal("clear did not restore legacy routing")
	}
}

func TestTaskWorkerModelsApplyOnlyOnResume(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("worker model resume", "local configuration only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := NewEngine(m) // no providers or tools run
	defer func() { e.StopTask(task.ID); m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	count := 2
	defaults := []int64{0}
	if _, err := m.UpdateTaskMetadata(task.ID, db.TaskPatch{WorkerCount: &count}); err != nil {
		t.Fatal(err)
	}
	e.Run(ctx, task)
	plan := func() map[string]WorkerSlot { p, _ := e.slotPlans.Load(task.ID); return p.(map[string]WorkerSlot) }
	if plan()["work#1"].TaskDefault {
		t.Fatal("legacy routing unexpectedly overridden")
	}
	if _, err := m.UpdateTaskMetadata(task.ID, db.TaskPatch{WorkerLLMProfileIDs: &defaults}); err != nil {
		t.Fatal(err)
	}
	if plan()["work#1"].TaskDefault {
		t.Fatal("edit changed an active generation")
	}
	e.Pause(task.ID, agent.AbortPausedByUser)
	e.Resume(task)
	if !plan()["work#1"].TaskDefault || !plan()["work#2"].TaskDefault {
		t.Fatal("unassigned Workers did not inherit task default on resume")
	}
	count = 3
	if _, err := m.UpdateTaskMetadata(task.ID, db.TaskPatch{WorkerCount: &count}); err != nil {
		t.Fatal(err)
	}
	e.Pause(task.ID, agent.AbortPausedByUser)
	e.Resume(task)
	if len(plan()) != 3 || !plan()["work#3"].TaskDefault {
		t.Fatal("new Worker did not inherit task default")
	}
	profile, err := m.pg.SaveProfile(&db.LLMProfile{Name: "resume-assigned", Format: "openai", Model: "local-test", APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteProfile(profile)
	defaults = []int64{0, profile}
	if _, err := m.UpdateTaskMetadata(task.ID, db.TaskPatch{WorkerLLMProfileIDs: &defaults}); err != nil {
		t.Fatal(err)
	}
	if plan()["work#2"].ProfileID != 0 {
		t.Fatal("assignment applied before resume")
	}
	e.Pause(task.ID, agent.AbortPausedByUser)
	e.Resume(task)
	if plan()["work#2"].ProfileID != profile || plan()["work#1"].ProfileID != 0 {
		t.Fatal("assignment did not apply to the selected ordinal")
	}
	count = 1
	if _, err := m.UpdateTaskMetadata(task.ID, db.TaskPatch{WorkerCount: &count}); err != nil {
		t.Fatal(err)
	}
	e.Pause(task.ID, agent.AbortPausedByUser)
	e.Resume(task)
	count = 2
	if _, err := m.UpdateTaskMetadata(task.ID, db.TaskPatch{WorkerCount: &count}); err != nil {
		t.Fatal(err)
	}
	e.Pause(task.ID, agent.AbortPausedByUser)
	e.Resume(task)
	if plan()["work#2"].ProfileID != profile {
		t.Fatal("reducing concurrency lost a dormant assignment")
	}
}

func TestCreateTaskWorkerModelsSavedBeforeFirstStart(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	id, err := m.pg.SaveProfile(&db.LLMProfile{Name: "create-worker-model", Format: "openai", Model: "local-model", APIKey: "test-secret", BaseURL: "http://127.0.0.1:1/v1"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteProfile(id)
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	defer func() { cancel(); s.Close() }()
	post := func(profiles string, want int) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"description":"local config only","goal":"no external actions","worker_count":2,"worker_llm_profile_ids":%s,"schedule":{"name":"test","schedule_type":"weekly","weekdays":[1],"start_time":"01:00","end_time":"02:00","timezone_mode":"beijing","start_immediately":false}}`, profiles)
		rec := httptest.NewRecorder()
		s.createTask(rec, httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body)))
		if rec.Code != want {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		return rec
	}
	var before int
	if err := m.pg.QueryRow("SELECT count(*) FROM tasks").Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`[-1]`, `[9223372036854775807]`, `[1.5]`, `"bad"`, `[` + strings.Repeat("0,", 50) + `0]`} {
		post(invalid, 400)
	}
	var after int
	if err := m.pg.QueryRow("SELECT count(*) FROM tasks").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("invalid model assignment left a task behind")
	}
	for _, profiles := range []string{fmt.Sprintf(`[%d,0]`, id), `[0,0]`, `[]`} {
		rec := post(profiles, 201)
		if strings.Contains(rec.Body.String(), "test-secret") {
			t.Fatal("credential leak")
		}
		var dto TaskDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		task, ok := m.Task(dto.ID)
		if !ok {
			t.Fatal("created task missing")
		}
		defer m.DeleteTask(task.ID, DeleteTaskOptions{})
		var expected []int64
		json.Unmarshal([]byte(profiles), &expected)
		stored, err := m.pg.GetTask(taskIDInt(t, task.ID))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(stored.WorkerLLMProfileIDs, expected) || !slices.Equal(dto.WorkerLLMProfileIDs, expected) {
			t.Fatal("create discarded Worker assignment")
		}
		if !stored.Paused {
			t.Fatal("scheduled test task started")
		}
		slots, err := workerSlotsForTask(task, nil, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(slots) != 2 {
			t.Fatal("wrong initial concurrency")
		}
		if len(expected) > 0 && (slots[0].ProfileID != expected[0] || !slots[1].TaskDefault) {
			t.Fatal("initial slots ignored creation assignments")
		}
	}
}
