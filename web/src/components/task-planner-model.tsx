"use client";

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { useUITranslation } from "@/i18n/use-ui-translation";
import { api } from "@/lib/api";
import type { LLMProfile, Task } from "@/lib/types";

export function TaskPlannerModel({ task, onUpdated }: { task: Task | null; onUpdated: (task: Task) => void }) {
  const { ui } = useUITranslation();
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [id, setID] = React.useState(0);
  const [dirty, setDirty] = React.useState(false);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState("");
  const [message, setMessage] = React.useState("");
  const taskID = task?.id ?? "";
  const savedID = task?.planner_llm_profile_id ?? 0;
  const previousTask = React.useRef(taskID);
  React.useEffect(() => {
    const changed = previousTask.current !== taskID;
    previousTask.current = taskID;
    if (dirty && !changed) return;
    setID(savedID);
    if (changed) { setDirty(false); setError(""); setMessage(""); }
  }, [taskID, savedID, dirty]);
  const load = React.useCallback(async () => {
    setLoading(true); setError("");
    try { setProfiles(await api.llmProfiles()); }
    catch (reason) { setError(reason instanceof Error ? reason.message : ui("taskUI.plannerModelLoadFailed")); }
    finally { setLoading(false); }
  }, [ui]);
  React.useEffect(() => { void load(); }, [load]);
  const edit = (next: number) => { setID(next); setDirty(true); setError(""); setMessage(""); };
  const save = async () => {
    if (!task) return;
    setSaving(true); setError(""); setMessage("");
    try {
      const updated = await api.updateTask(task.id, { planner_llm_profile_id: id });
      onUpdated(updated); setDirty(false); setMessage(ui("taskUI.plannerModelSaved"));
    } catch (reason) { setError(reason instanceof Error ? reason.message : ui("taskUI.llmSettingsSaveFailed")); }
    finally { setSaving(false); }
  };
  const missing = id > 0 && !profiles.some((profile) => Number(profile.id) === id);
  const disabled = !task || loading || saving;
  return <Card>
    <CardHeader>
      <CardTitle className="text-base">{ui("taskUI.plannerModelTitle")}</CardTitle>
      <CardDescription>{ui("taskUI.plannerModelHelp")}</CardDescription>
    </CardHeader>
    <CardContent className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <label htmlFor={`planner-model-${taskID}`} className="min-w-24 text-sm font-medium">Planner</label>
        <NativeSelect id={`planner-model-${taskID}`} className="min-w-64 max-w-full" value={String(id)} disabled={disabled} onChange={(event) => edit(Number(event.target.value))}>
          <NativeSelectOption value="0">{ui("taskUI.plannerModelDefault")}</NativeSelectOption>
          {missing && <NativeSelectOption value={String(id)}>{ui("taskUI.workerModelsMissing", { id })}</NativeSelectOption>}
          {profiles.map((profile) => <NativeSelectOption key={profile.id} value={String(profile.id)}>{profile.name} · {profile.model}</NativeSelectOption>)}
        </NativeSelect>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button disabled={disabled} onClick={() => void save()}>{ui("taskUI.plannerModelSave")}</Button>
        <Button variant="outline" disabled={loading || saving} onClick={() => void load()}>{ui("taskUI.llmSettingsRefresh")}</Button>
        <Button variant="ghost" disabled={disabled} onClick={() => edit(0)}>{ui("taskUI.plannerModelReset")}</Button>
      </div>
      {message && <p role="status" className="text-sm text-muted-foreground">{message}</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </CardContent>
  </Card>;
}
