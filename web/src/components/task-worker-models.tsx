"use client";

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { WorkerModelPicker } from "@/components/worker-model-picker";
import { useUITranslation } from "@/i18n/use-ui-translation";
import { api } from "@/lib/api";
import type { LLMProfile, Task } from "@/lib/types";

export function TaskWorkerModels({ task, onUpdated }: { task: Task | null; onUpdated: (task: Task) => void }) {
  const { ui } = useUITranslation();
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [globalWorkers, setGlobalWorkers] = React.useState<number | null>(null);
  const [ids, setIDs] = React.useState<number[]>([]);
  const [dirty, setDirty] = React.useState(false);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState("");
  const [message, setMessage] = React.useState("");
  const taskID = task?.id ?? "";
  const savedKey = JSON.stringify(task?.worker_llm_profile_ids ?? []);
  const previousTask = React.useRef(taskID);
  const count = Math.min(50, Math.max(1, task?.worker_count || globalWorkers || 3));

  React.useEffect(() => {
    const changed = previousTask.current !== taskID;
    previousTask.current = taskID;
    if (dirty && !changed) return;
    setIDs(JSON.parse(savedKey));
    if (changed) { setDirty(false); setError(""); setMessage(""); }
  }, [taskID, savedKey, dirty]);

  const load = React.useCallback(async () => {
    setLoading(true); setError("");
    try {
      const [nextProfiles, settings] = await Promise.all([api.llmProfiles(), api.settings()]);
      setProfiles(nextProfiles); setGlobalWorkers(settings.workers ?? 3);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : ui("taskUI.workerModelsLoadFailed"));
    } finally { setLoading(false); }
  }, [ui]);
  React.useEffect(() => { void load(); }, [load]);

  const edit = () => { setDirty(true); setMessage(""); setError(""); };
  const save = async () => {
    if (!task) return;
    setSaving(true); setError(""); setMessage("");
    try {
      const next = Array.from({ length: Math.max(count, ids.length) }, (_, i) => ids[i] ?? 0);
      const updated = await api.updateTask(task.id, { worker_llm_profile_ids: next });
      onUpdated(updated); setDirty(false); setMessage(ui("taskUI.workerSettingsSaved"));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : ui("taskUI.llmSettingsSaveFailed"));
    } finally { setSaving(false); }
  };

  return <Card>
    <CardHeader>
      <CardTitle className="text-base">{ui("taskUI.workerModelsTitle")}</CardTitle>
      <CardDescription>{ui("taskUI.workerModelsHelp")}</CardDescription>
    </CardHeader>
    <CardContent className="flex flex-col gap-3">
      <WorkerModelPicker profiles={profiles} count={count} value={ids} idPrefix={`worker-model-${taskID}`} disabled={!task || loading || saving}
        onChange={(next) => { edit(); setIDs(next); }} />
      <p className="text-xs text-muted-foreground">{ui("taskUI.workerModelsScope")}</p>
      <div className="flex flex-wrap items-center gap-2">
        <Button disabled={!task || loading || saving} onClick={() => void save()}>{ui("taskUI.workerModelsSave")}</Button>
        <Button variant="outline" disabled={loading || saving} onClick={() => void load()}>{ui("taskUI.llmSettingsRefresh")}</Button>
        <Button variant="ghost" disabled={!task || loading || saving} onClick={() => { edit(); setIDs(Array(count).fill(0)); }}>{ui("taskUI.workerModelsAllDefault")}</Button>
      </div>
      {message && <p role="status" className="text-sm text-muted-foreground">{message}</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </CardContent>
  </Card>;
}
