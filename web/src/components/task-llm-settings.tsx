"use client";

import * as React from "react";
import { TaskLLMProfileChain } from "@/components/task-llm-profile-chain";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { useUITranslation } from "@/i18n/use-ui-translation";
import { api } from "@/lib/api";
import type { LLMProfile, Task } from "@/lib/types";

export function TaskLLMSettings({ task, onUpdated }: { task: Task | null; onUpdated: (patch: Partial<Task>) => void }) {
  const { ui } = useUITranslation();
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [profileIDs, setProfileIDs] = React.useState<string[]>([]);
  const [activeID, setActiveID] = React.useState("");
  const [dirty, setDirty] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState("");
  const [message, setMessage] = React.useState("");
  const taskID = task?.id ?? "";
  const savedIDs = task?.llm_profile_ids?.length ? task.llm_profile_ids : task?.llm_profile_id ? [task.llm_profile_id] : [];
  const savedKey = JSON.stringify(savedIDs);
  const savedActive = task?.active_llm_profile_id ? String(task.active_llm_profile_id) : "";
  const previousTaskID = React.useRef(taskID);

  React.useEffect(() => {
    const changedTask = previousTaskID.current !== taskID;
    previousTaskID.current = taskID;
    if (dirty && !changedTask) return;
    const next: string[] = JSON.parse(savedKey).map(String);
    setProfileIDs(next);
    setActiveID(savedActive || next[0] || "");
    if (changedTask) { setDirty(false); setError(""); setMessage(""); }
  }, [taskID, savedKey, savedActive, dirty]);

  const loadProfiles = React.useCallback(async () => {
    setLoading(true);
    setError("");
    try { setProfiles(await api.llmProfiles()); }
    catch (reason) { setError(reason instanceof Error ? reason.message : ui("taskUI.llmSettingsLoadFailed")); }
    finally { setLoading(false); }
  }, [ui]);
  React.useEffect(() => { void loadProfiles(); }, [loadProfiles]);

  const edit = () => { setDirty(true); setMessage(""); setError(""); };
  const save = async () => {
    if (!task) return;
    setSaving(true); setError(""); setMessage("");
    try {
      const selected = profileIDs.includes(activeID) ? activeID : profileIDs[0];
      const result = await api.updateTaskLLMProfiles(task.id, profileIDs.map(Number), selected ? Number(selected) : undefined);
      const next = result.llm_profile_ids ?? [];
      onUpdated({
        llm_profile_ids: next,
        llm_profile_id: next[0],
        active_llm_profile_id: result.active_llm_profile_id,
        llm_failover_state: result.llm_failover_state,
      });
      setDirty(false);
      setMessage(ui("taskUI.llmSettingsSaved"));
    } catch (reason) { setError(reason instanceof Error ? reason.message : ui("taskUI.llmSettingsSaveFailed")); }
    finally { setSaving(false); }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{ui("taskUI.llmSettingsTitle")}</CardTitle>
        <CardDescription>{ui("taskUI.llmSettingsHelp")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <TaskLLMProfileChain
          profiles={profiles}
          value={profileIDs}
          onValueChange={(next) => { edit(); setProfileIDs(next); setActiveID((current) => next.includes(current) ? current : next[0] ?? ""); }}
          activeProfileId={activeID}
          onActiveProfileChange={(next) => { edit(); setActiveID(next); }}
          inputId={`task-overview-llm-${taskID}`}
          disabled={!task || loading || saving}
        />
        <p className="text-xs text-muted-foreground">{ui("taskUI.llmSettingsBindings")}</p>
        <div className="flex flex-wrap items-center gap-2">
          <Button disabled={!task || loading || saving} onClick={() => void save()}>{ui("taskUI.llmSettingsSave")}</Button>
          <Button variant="outline" disabled={loading || saving} onClick={() => void loadProfiles()}>{ui("taskUI.llmSettingsRefresh")}</Button>
          <Button variant="ghost" disabled={!task || saving || loading || profileIDs.length === 0} onClick={() => { edit(); setProfileIDs([]); setActiveID(""); }}>{ui("taskUI.llmSettingsGlobal")}</Button>
        </div>
        {message && <p role="status" className="text-sm text-muted-foreground">{message}</p>}
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      </CardContent>
    </Card>
  );
}
