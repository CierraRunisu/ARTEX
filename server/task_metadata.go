package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

const (
	maxTaskNameRunes           = 200
	maxTaskMetadataRequestSize = 16 << 10
)

// updateTaskMetadata updates editable metadata and live task policies.
func (s *Server) updateTaskMetadata(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if _, ok := s.m.Task(taskID); !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskMetadataRequestSize)
	var request struct {
		Name                *string  `json:"name"`
		Remark              *string  `json:"remark"`
		Pinned              *bool    `json:"pinned"`
		ShareGlobalAssets   *bool    `json:"share_global_assets"`
		WorkerCount         *int     `json:"worker_count"`
		WorkerLLMProfileIDs *[]int64 `json:"worker_llm_profile_ids"`
	}
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if request.Name == nil && request.Pinned == nil && request.Remark == nil && request.ShareGlobalAssets == nil && request.WorkerCount == nil && request.WorkerLLMProfileIDs == nil {
		writeErr(w, http.StatusBadRequest, "至少需要提供 name、pinned、remark、share_global_assets 、worker_count 或 worker_llm_profile_ids")
		return
	}
	if request.WorkerCount != nil && (*request.WorkerCount < 0 || *request.WorkerCount > db.MaxTaskWorkerCount) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("Worker 数量必须在 0 到 %d 之间", db.MaxTaskWorkerCount))
		return
	}
	if request.WorkerLLMProfileIDs != nil {
		if err := s.m.pg.ValidateWorkerLLMProfileIDs(*request.WorkerLLMProfileIDs); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "任务名称不能为空")
			return
		}
		if utf8.RuneCountInString(name) > maxTaskNameRunes {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("任务名称最多 %d 个字符", maxTaskNameRunes))
			return
		}
		request.Name = &name
	}

	if request.Remark != nil {
		remark, err := normalizeRemark(*request.Remark)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		request.Remark = &remark
	}
	// Serialize edits with admission so a successful edit before resume is the
	// exact configuration used to rebuild that task's worker pool.
	s.concMu.Lock()
	defer s.concMu.Unlock()
	task, err := s.m.UpdateTaskMetadata(taskID, db.TaskPatch{Name: request.Name, Pinned: request.Pinned, Remark: request.Remark, ShareGlobalAssets: request.ShareGlobalAssets, WorkerCount: request.WorkerCount, WorkerLLMProfileIDs: request.WorkerLLMProfileIDs})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if request.WorkerCount != nil || request.WorkerLLMProfileIDs != nil {
		s.engine.ReconcileWorkers(task)
	}
	dto := taskDTO(task, s.resolvedTaskStatus(task))
	dto.WorkerActualCount, dto.WorkerTargetCount = s.engine.WorkerCounts(task)
	writeJSON(w, http.StatusOK, dto)
}
