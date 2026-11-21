import type { Activity, Session, TaskLLMResolution, TaskLLMResolutions } from "./types";

// Session/intent IDs are unrelated to pool slot numbers. Prefer the node's owner;
// activity is a fallback for deep links whose node has not loaded yet.
export function workerSessionSlot(session: Session, items: Pick<Activity, "worker">[]): string | undefined {
  if (session.worker) return /^work#[1-9]\d*$/.test(session.worker) ? session.worker : undefined;
  for (let i = items.length - 1; i >= 0; i--) {
    if (/^work#[1-9]\d*$/.test(items[i].worker)) return items[i].worker;
  }
  return undefined;
}

export function workerSessionResolution(
  resolutions: TaskLLMResolutions,
  session: Session,
  items: Pick<Activity, "worker">[],
): TaskLLMResolution | undefined {
  if (session.role !== "worker" || session.inherited) return undefined;
  const slot = workerSessionSlot(session, items);
  // Do not substitute the common role label when a slot is unknown or removed.
  // Older servers can still provide their legacy role-only response.
  if (resolutions.workers) return slot ? resolutions.workers[slot] : undefined;
  return resolutions.worker;
}
