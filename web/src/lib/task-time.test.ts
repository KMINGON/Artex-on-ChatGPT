import { expect, it } from "vitest";

import type { Task } from "@/lib/types";

import { taskDuration } from "./task-time";

const task: Task = { id: "1", description: "", goal: "", status: "running", created_at: "", created_unix: 100 };
it("excludes queue wait before the first model run and uses completion time", () => {
  expect(taskDuration({ ...task, first_run_unix: 300 }, 400)).toBe(100);
  expect(taskDuration({ ...task, status: "done", first_run_unix: 300, completed_unix: 500 }, 900)).toBe(200);
});
it("keeps the historical fallback and never invents a duration without activity", () => {
  expect(taskDuration(task, 400)).toBe(300);
  expect(taskDuration({ ...task, status: "created" }, 400)).toBe(0);
  expect(taskDuration({ ...task, status: "paused", first_run_unix: 300, last_activity_unix: 400 }, 900)).toBe(100);
});
