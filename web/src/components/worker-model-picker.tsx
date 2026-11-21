"use client";

import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { useUITranslation } from "@/i18n/use-ui-translation";
import type { LLMProfile } from "@/lib/types";

export function WorkerModelPicker({ profiles, count, value, onChange, idPrefix, disabled }: {
  profiles: LLMProfile[]; count: number; value: number[]; onChange: (value: number[]) => void; idPrefix: string; disabled?: boolean;
}) {
  const { ui } = useUITranslation();
  return <div className="flex flex-col gap-3">
    {Array.from({ length: Math.min(50, Math.max(1, count)) }, (_, index) => {
      const id = value[index] ?? 0;
      const missing = id > 0 && !profiles.some((p) => String(p.id) === String(id));
      const inputID = `${idPrefix}-${index}`;
      return <div key={index} className="flex flex-wrap items-center gap-3">
        <label htmlFor={inputID} className="min-w-24 text-sm font-medium">Worker #{index + 1}</label>
        <NativeSelect id={inputID} className="min-w-64 max-w-full" value={String(id)} disabled={disabled}
          onChange={(event) => onChange(Array.from({ length: Math.max(value.length, index + 1) }, (_, i) => i === index ? Number(event.target.value) : value[i] ?? 0))}>
          <NativeSelectOption value="0">{ui("taskUI.workerModelsDefault")}</NativeSelectOption>
          {missing && <NativeSelectOption value={String(id)}>{ui("taskUI.workerModelsMissing", { id })}</NativeSelectOption>}
          {profiles.map((profile) => <NativeSelectOption key={profile.id} value={String(profile.id)}>{profile.name} · {profile.model}</NativeSelectOption>)}
        </NativeSelect>
      </div>;
    })}
  </div>;
}
