import type { Task } from "@/lib/types";

// The first model run starts the clock; old tasks without that stamp retain the
// historical creation-time fallback. Pauses still count as elapsed wall time.
export function taskDuration(task: Task, nowSec: number): number {
  const firstRun = task.first_run_unix ?? 0;
  const start = firstRun > 0 ? firstRun : (task.created_unix ?? 0);
  if (!start) return 0;
  const end =
    task.status === "running"
      ? nowSec
      : task.completed_unix && task.completed_unix > 0
        ? task.completed_unix
        : (task.last_activity_unix ?? 0);
  return end > start ? end - start : 0;
}
