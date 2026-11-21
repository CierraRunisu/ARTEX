import { test } from "node:test";
import assert from "node:assert/strict";
import { workerSessionResolution, workerSessionSlot } from "./worker-session-resolution.ts";

const common = { name: "task", model: "task-model", available: true };
const pinned = { name: "pinned", model: "pinned-model", available: true };
const inherited = { name: "global", model: "global-model", available: true };
const resolutions = { worker: common, workers: { "work#1": pinned, "work#2": inherited } };
const session = { role: "worker", id: "987", intent_id: "987", worker: "work#2" };

test("uses actual slot instead of intent ID, list order or task model", () => {
  assert.equal(workerSessionResolution(resolutions, session, []), inherited);
  assert.equal(workerSessionResolution(resolutions, { ...session, worker: "work#1" }, []), pinned);
});
test("deep links recover slot from most recent worker activity", () => {
  const items = [{worker: "work#1"}, {worker: "system"}, {worker: "work#2"}];
  assert.equal(workerSessionSlot({...session, worker: undefined}, items), "work#2");
});
test("unknown, removed, inherited and chat-owned sessions do not claim a task model", () => {
  for (const worker of [undefined, "work#3", "chat"]) {
    assert.equal(workerSessionResolution(resolutions, {...session, worker}, []), undefined);
  }
  assert.equal(workerSessionResolution(resolutions, {...session, inherited: true}, []), undefined);
});
test("unavailable explicit profile stays unavailable without task fallback", () => {
  const missing = {available: false, profile_id: 99, reason: "missing"};
  assert.equal(workerSessionResolution({...resolutions, workers: {"work#2": missing}}, session, []), missing);
});
test("supports old role-only API", () => {
  assert.equal(workerSessionResolution({worker: common}, session, []), common);
});
