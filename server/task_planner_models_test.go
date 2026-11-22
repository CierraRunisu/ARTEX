package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func requirePlannerTestDB(t *testing.T) {
	t.Helper()
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires explicitly configured isolated ARTEX_PG_DSN")
	}
}

func TestTaskPlannerModelLifecycleDTO(t *testing.T) {
	task := taskFromPG(&db.Task{ID: 17, PlannerLLMProfileID: 41}, nil, nil)
	if task.lifecycleSnapshot().PlannerLLMProfileID != 41 {
		t.Fatal("reload lost Planner assignment")
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.PlannerLLMProfileID = 42 })
	dto := taskDTO(task, "paused")
	if dto.PlannerLLMProfileID != 42 || task.lifecycleSnapshot().PlannerLLMProfileID != 42 {
		t.Fatal("Planner assignment missing from lifecycle/DTO")
	}
}

func TestTaskPlannerModelPersistenceAndLiveRouting(t *testing.T) {
	requirePlannerTestDB(t)
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ids := make([]int64, 3)
	for i := range ids {
		ids[i], err = m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("planner-test-%d", i), Format: "openai", Model: fmt.Sprintf("planner-model-%d", i), APIKey: "planner-secret", BaseURL: "http://127.0.0.1:1/v1"})
		if err != nil {
			t.Fatal(err)
		}
		defer m.pg.DeleteProfile(ids[i])
	}
	task, err := m.CreateTaskWithOptions("planner configuration only", "no external actions", db.TaskCreateOptions{LLMProfileIDs: []int64{ids[2]}, PlannerLLMProfileID: ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	if err := m.ApplyTaskPause(task.ID); err != nil {
		t.Fatal(err)
	}
	old, err := m.pg.GetAgentByKey("planner")
	if err != nil {
		t.Fatal(err)
	}
	var previous *int64
	if old != nil {
		previous = old.LLMProfileID
	}
	defer m.pg.SetAgentLLMProfile("planner", previous)
	if err := m.pg.SetAgentLLMProfile("planner", &ids[1]); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, provByProfile: map[int64]*provEntry{}}
	providers := make([]*scriptedLLMProvider, 3)
	for i, id := range ids {
		providers[i] = &scriptedLLMProvider{}
		s.provByProfile[id] = &provEntry{prov: providers[i], cfg: agent.Config{Model: fmt.Sprintf("planner-model-%d", i), MaxTokens: 111 + i, ContextWindowK: 32 + i}}
	}
	runtime := &taskLLMRuntime{s: s, taskID: task.ID, agentKey: "planner"}
	patch := func(body string, want int) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader(body))
		req.SetPathValue("id", task.ID)
		rec := httptest.NewRecorder()
		s.updateTaskMetadata(rec, req)
		if rec.Code != want {
			t.Fatalf("patch %s: %d %s", body, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	check := func(index int, source string) {
		t.Helper()
		sel, err := runtime.current()
		if err != nil || sel.provider != providers[index] {
			t.Fatalf("provider precedence index=%d selection=%+v err=%v", index, sel, err)
		}
		if sel.profileID != 0 {
			t.Fatal("assigned/bound Planner incorrectly participates in task failover")
		}
		cfg, ok := runtime.activeCfg()
		if !ok || cfg.Model != fmt.Sprintf("planner-model-%d", index) || runtime.maxTokens() != 111+index || runtime.CompactionWindow() != cfg.CompactionWindow() {
			t.Fatalf("runtime settings differ: %+v %v", cfg, ok)
		}
		resolved, err := s.resolveTaskRoleLLM(task, "planner")
		if err != nil || !resolved.Available || resolved.ProfileID == nil || *resolved.ProfileID != ids[index] || resolved.Source != source {
			t.Fatalf("resolution disagrees: %+v %v", resolved, err)
		}
	}
	check(0, "planner_assignment")
	patch(fmt.Sprintf(`{"planner_llm_profile_id":%d}`, ids[1]), 200)
	check(1, "planner_assignment") // same runtime instance sees the next-call change
	for _, body := range []string{`{"planner_llm_profile_id":-1}`, `{"planner_llm_profile_id":9223372036854775807}`, `{"planner_llm_profile_id":1.5}`, `{"planner_llm_profile_id":"bad"}`} {
		patch(body, 400)
	}
	check(1, "planner_assignment")
	patch(`{"remark":"unrelated edit"}`, 200)
	check(1, "planner_assignment")
	body := patch(fmt.Sprintf(`{"planner_llm_profile_id":%d}`, ids[0]), 200)
	if strings.Contains(body, "planner-secret") {
		t.Fatal("credential leak")
	}
	stored, err := m.pg.GetTask(taskIDInt(t, task.ID))
	if err != nil || stored.PlannerLLMProfileID != ids[0] || !stored.Paused {
		t.Fatalf("save changed pause or lost assignment: %+v %v", stored, err)
	}
	if taskFromPG(stored, nil, nil).lifecycleSnapshot().PlannerLLMProfileID != ids[0] {
		t.Fatal("reload lost assignment")
	}
	before := stored.LLMChainRevision
	providers[0].err = errors.New("insufficient_quota")
	if _, _, _, err := runtime.Complete(context.Background(), llm.CompletionRequest{}); err == nil {
		t.Fatal("expected quota failure")
	}
	stored, err = m.pg.GetTask(taskIDInt(t, task.ID))
	if err != nil || stored.LLMChainRevision != before || stored.ActiveLLMProfileID == nil || *stored.ActiveLLMProfileID != ids[2] {
		t.Fatal("Planner quota failure advanced task chain")
	}
	if err := m.pg.DeleteProfile(ids[0]); err != nil {
		t.Fatal(err)
	}
	delete(s.provByProfile, ids[0])
	if _, err := runtime.current(); err == nil || !strings.Contains(err.Error(), "Planner LLM profile") {
		t.Fatalf("deleted assignment fell back: %v", err)
	}
	if _, ok := runtime.activeCfg(); ok {
		t.Fatal("deleted assignment exposed fallback settings")
	}
	resolved, err := s.resolveTaskRoleLLM(task, "planner")
	if err != nil || resolved.Available || resolved.ProfileID == nil || *resolved.ProfileID != ids[0] || resolved.Source != "planner_assignment" || resolved.Reason == "" {
		t.Fatalf("deleted assignment resolution: %+v %v", resolved, err)
	}
	patch(`{"planner_llm_profile_id":0}`, 200)
	check(1, "agent_binding")
	if err := m.pg.SetAgentLLMProfile("planner", nil); err != nil {
		t.Fatal(err)
	}
	sel, err := runtime.current()
	if err != nil || sel.provider != providers[2] || sel.profileID != ids[2] {
		t.Fatalf("inherit did not restore task chain: %+v %v", sel, err)
	}
}

func TestCreateTaskPlannerModelSavedBeforeStart(t *testing.T) {
	requirePlannerTestDB(t)
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	id, err := m.pg.SaveProfile(&db.LLMProfile{Name: "create-planner-test", Format: "openai", Model: "local-only", APIKey: "planner-secret", BaseURL: "http://127.0.0.1:1/v1"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteProfile(id)
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	defer func() { cancel(); s.Close() }()
	post := func(value string, want int) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"description":"local configuration only","goal":"no external actions","planner_llm_profile_id":%s,"schedule":{"name":"test","schedule_type":"weekly","weekdays":[1],"start_time":"01:00","end_time":"02:00","timezone_mode":"beijing","start_immediately":false}}`, value)
		rec := httptest.NewRecorder()
		s.createTask(rec, httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body)))
		if rec.Code != want {
			t.Fatalf("create %s: %d %s", value, rec.Code, rec.Body.String())
		}
		return rec
	}
	var before int
	if err := m.pg.QueryRow("SELECT count(*) FROM tasks").Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"-1", "9223372036854775807", "1.5", `"bad"`} {
		post(invalid, 400)
	}
	var after int
	if err := m.pg.QueryRow("SELECT count(*) FROM tasks").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("invalid create left a task behind")
	}
	for _, value := range []int64{id, 0} {
		rec := post(fmt.Sprint(value), 201)
		var dto TaskDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		defer m.DeleteTask(dto.ID, DeleteTaskOptions{})
		stored, err := m.pg.GetTask(taskIDInt(t, dto.ID))
		if err != nil || stored.PlannerLLMProfileID != value || dto.PlannerLLMProfileID != value || !stored.Paused {
			t.Fatalf("initial Planner assignment/pause lost: %+v %+v %v", stored, dto, err)
		}
		if strings.Contains(rec.Body.String(), "planner-secret") {
			t.Fatal("credential leak")
		}
	}
}
