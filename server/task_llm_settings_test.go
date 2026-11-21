package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestTaskLLMSettingsSwitchPreservesPauseAndTerminalState(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatalf("test database unavailable: %v", err)
	}
	defer m.Close()
	var profiles []int64
	for _, name := range []string{"task-settings-model-a", "task-settings-model-b"} {
		id, err := m.pg.SaveProfile(&db.LLMProfile{Name: name, Format: "openai", Model: name, APIKey: "unit-test-only-key", BaseURL: "http://127.0.0.1:1/v1"})
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, id)
		defer m.pg.DeleteProfile(id)
	}
	task, err := m.CreateTask("task model settings", "local configuration test", nil, 0, 0)
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
	put := func(body string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+task.ID+"/llm", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
		}
	}
	for _, id := range profiles {
		put(fmt.Sprintf(`{"llm_profile_ids":[%d],"active_llm_profile_id":%d}`, id, id), 200)
		state := task.llmStateSnapshot()
		if state.ActiveID == nil || *state.ActiveID != id {
			t.Fatalf("new model not persisted: %+v", state)
		}
		if !task.lifecycleSnapshot().Paused {
			t.Fatal("editing a model resumed a paused task")
		}
		selected, err := (&taskLLMRuntime{s: s, taskID: task.ID, agentKey: "worker"}).current()
		if err != nil || selected.profileID != id {
			t.Fatalf("next call resolves profile=%d, want %d: %v", selected.profileID, id, err)
		}
	}
	before := task.llmStateSnapshot()
	put(`{"llm_profile_ids":[9223372036854775807]}`, 400)
	after := task.llmStateSnapshot()
	if !sameOptionalID(before.ActiveID, after.ActiveID) {
		t.Fatal("invalid selection changed active model")
	}
	if _, err := m.SetTaskStatusGuarded(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	put(fmt.Sprintf(`{"llm_profile_ids":[%d],"active_llm_profile_id":%d}`, profiles[0], profiles[0]), 200)
	if task.lifecycleSnapshot().Status != "done" {
		t.Fatal("model edit reopened completed task")
	}
	put(`{"llm_profile_ids":[],"active_llm_profile_id":null}`, 200)
	if len(task.llmStateSnapshot().ProfileIDs) != 0 {
		t.Fatal("clearing model selection did not restore inheritance")
	}
}
