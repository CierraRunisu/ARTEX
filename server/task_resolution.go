package server

import (
	"fmt"
	"net/http"

	"github.com/Autumn-27/artex/db"
)

type taskLLMResolution struct {
	ProfileID *int64 `json:"profile_id,omitempty"`
	Name      string `json:"name"`
	Format    string `json:"format"`
	Model     string `json:"model"`
	Source    string `json:"source"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func (s *Server) resolutionFromProfile(p *db.LLMProfile, source string) taskLLMResolution {
	if p == nil {
		return taskLLMResolution{Source: source, Reason: "LLM 配置不存在"}
	}
	id := p.ID
	result := taskLLMResolution{
		ProfileID: &id,
		Name:      p.Name,
		Format:    p.Format,
		Model:     p.Model,
		Source:    source,
	}
	if p.APIKey == "" {
		result.Reason = "LLM 配置未设置 API Key"
		return result
	}
	if _, _, ok := s.providerForProfile(p.ID); !ok {
		result.Reason = "LLM 配置格式或参数无效"
		return result
	}
	result.Available = true
	return result
}

// resolveTaskRoleLLM mirrors taskLLMRuntime.current without creating a provider:
// role Agent binding -> explicit task chain -> active/global environment config.
// Database failures are returned rather than represented as an unavailable model.
func (s *Server) resolveTaskRoleLLM(t *Task, agentKey string) (taskLLMResolution, error) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return taskLLMResolution{}, fmt.Errorf("database unavailable")
	}
	a, err := s.m.pg.GetAgentByKey(agentKey)
	if err != nil {
		return taskLLMResolution{}, fmt.Errorf("load agent %q: %w", agentKey, err)
	}
	if a != nil && a.LLMProfileID != nil {
		p, err := s.m.pg.ProfileByID(*a.LLMProfileID)
		if err != nil {
			return taskLLMResolution{}, fmt.Errorf("load agent LLM profile: %w", err)
		}
		if p != nil {
			resolved := s.resolutionFromProfile(p, "agent_binding")
			if resolved.Available {
				return resolved, nil
			}
		}
	}
	return s.resolveTaskDefaultLLM(t)
}

// Task-default slots deliberately bypass the legacy Worker role binding.
func (s *Server) resolveTaskDefaultLLM(t *Task) (taskLLMResolution, error) {
	state := t.llmStateSnapshot()
	if len(state.ProfileIDs) > 0 {
		if state.ActiveID == nil {
			return taskLLMResolution{Source: "task_chain", Reason: "任务 LLM 配置链额度已耗尽"}, nil
		}
		p, err := s.m.pg.ProfileByID(*state.ActiveID)
		if err != nil {
			return taskLLMResolution{}, fmt.Errorf("load task LLM profile: %w", err)
		}
		return s.resolutionFromProfile(p, "task_chain"), nil
	}
	p, err := s.m.pg.ActiveProfile()
	if err != nil {
		return taskLLMResolution{}, fmt.Errorf("load global LLM profile: %w", err)
	}
	if p != nil {
		resolved := s.resolutionFromProfile(p, "global_profile")
		if resolved.Available {
			return resolved, nil
		}
	}
	s.cfgMu.Lock()
	on, name, cfg := s.llmOn, s.llmProf, s.llmCfg
	s.cfgMu.Unlock()
	if on {
		if name == "" {
			name = "全局配置"
		}
		return taskLLMResolution{
			Name: name, Format: cfg.Provider(), Model: cfg.Model,
			Source: "environment", Available: true,
		}, nil
	}
	return taskLLMResolution{Source: "global", Reason: "没有可用的 LLM 配置"}, nil
}

// workerResolutionSlots reports the live generations, including draining slots.
// Historical slotPlans are reserved for intent continuation, not live labels.
func (s *Server) workerResolutionSlots(t *Task) (map[string]WorkerSlot, bool, error) {
	state := t.lifecycleSnapshot()
	if s.engine != nil && !state.Queued && state.CompletedAt == 0 {
		if value, ok := s.engine.workerPools.Load(t.ID); ok {
			pool := value.(*taskWorkerPool)
			pool.mu.Lock()
			defer pool.mu.Unlock()
			plan := make(map[string]WorkerSlot, len(pool.loops))
			for name, loop := range pool.loops {
				plan[name] = loop.slot
			}
			return plan, true, nil
		}
	}
	pools, count := []WorkerPool(nil), state.WorkerCount
	if count <= 0 {
		pools, count = s.m.WorkerPools(), s.m.Workers()
	}
	slots, err := workerSlotsForTask(t, pools, count)
	if err != nil {
		return nil, false, err
	}
	plan := make(map[string]WorkerSlot, len(slots))
	for _, slot := range slots {
		plan[slot.Name] = slot
	}
	return plan, false, nil
}

func (s *Server) resolveWorkerSlotLLM(t *Task, slot WorkerSlot) (taskLLMResolution, error) {
	if slot.ProfileID > 0 {
		p, err := s.m.pg.ProfileByID(slot.ProfileID)
		if err != nil {
			return taskLLMResolution{}, fmt.Errorf("load worker LLM profile: %w", err)
		}
		resolved := s.resolutionFromProfile(p, "worker_assignment")
		// Preserve a deleted assignment's ID; never imply a fallback was selected.
		id := slot.ProfileID
		resolved.ProfileID = &id
		return resolved, nil
	}
	if slot.TaskDefault {
		return s.resolveTaskDefaultLLM(t)
	}
	return s.resolveTaskRoleLLM(t, "worker")
}

func (s *Server) taskLLMResolutionHandler(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	roles := map[string]any{}
	for responseKey, agentKey := range map[string]string{
		"mainagent": "mainagent",
		"planner":   "planner",
		"worker":    "worker",
	} {
		resolved, err := s.resolveTaskRoleLLM(t, agentKey)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		roles[responseKey] = resolved
	}
	plan, applied, err := s.workerResolutionSlots(t)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	workers := make(map[string]taskLLMResolution, len(plan))
	for name, slot := range plan {
		resolved, err := s.resolveWorkerSlotLLM(t, slot)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		workers[name] = resolved
	}
	roles["workers"] = workers
	roles["workers_applied"] = applied
	// A live snapshot can still differ from the saved target while old calls drain.
	if applied {
		value, ok := s.engine.workerPools.Load(t.ID)
		if ok {
			pool := value.(*taskWorkerPool)
			pool.mu.Lock()
			converged := len(pool.loops) == len(pool.desired)
			pending := make(map[string]WorkerSlot, len(pool.desired))
			for name, slot := range pool.desired {
				pending[name] = slot
				actual, exists := plan[name]
				if !exists || actual != slot {
					converged = false
				}
			}
			for _, loop := range pool.loops {
				if loop.idleCtx.Err() != nil {
					converged = false
				}
			}
			pool.mu.Unlock()
			roles["workers_converged"] = converged
			if !converged {
				resolutions := make(map[string]taskLLMResolution, len(pending))
				for name, slot := range pending {
					resolved, err := s.resolveWorkerSlotLLM(t, slot)
					if err != nil {
						writeErr(w, http.StatusInternalServerError, err.Error())
						return
					}
					resolutions[name] = resolved
				}
				roles["workers_pending"] = resolutions
			}
		}
	}
	writeJSON(w, http.StatusOK, roles)
}
