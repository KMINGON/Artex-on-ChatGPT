import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, expect, it } from "vitest";

import type { Task } from "@/lib/types";

import en from "../../messages/en.json";
import ko from "../../messages/ko.json";
import { TaskRuntimeSummary } from "./task-runtime-summary";

afterEach(cleanup);
const task: Task = {
  id: "1",
  description: "",
  goal: "",
  status: "running",
  created_at: "",
  runtime: {
    workers: 5,
    worker_source: "task_snapshot",
    browser_limit: 8,
    browser_enabled: true,
    browser_worker_visible: null,
    traffic_capture: false,
    models: {
      worker: {
        name: "profile",
        format: "openai",
        model: "configured-model",
        source: "task_chain",
        available: true,
        requested_program: "daybreak_blue",
        reasoning_effort: "medium",
      },
    },
  },
};
it("labels requested settings without treating enabled MCP as browser readiness", () => {
  render(
    <NextIntlClientProvider locale="ko" messages={ko}>
      <TaskRuntimeSummary task={task} />
    </NextIntlClientProvider>,
  );
  expect(screen.getByText("5개 · 작업 생성 시 저장값")).toBeInTheDocument();
  expect(screen.getByText("MCP 켜짐 · Worker 직접 접근 미확인")).toBeInTheDocument();
  expect(screen.getByText("configured-model")).toBeInTheDocument();
  expect(screen.getByText("daybreak_blue")).toBeInTheDocument();
  expect(screen.getByText(ko.taskDetail.runtime.hint)).toBeInTheDocument();
  expect(screen.getByText(ko.taskDetail.runtime.browserHint)).toBeInTheDocument();
});
it("provides the same configuration meaning in English", () => {
  render(
    <NextIntlClientProvider locale="en" messages={en}>
      <TaskRuntimeSummary task={task} />
    </NextIntlClientProvider>,
  );
  expect(screen.getByText("5 · saved with task")).toBeInTheDocument();
  expect(screen.getByText("MCP on · direct worker access unknown")).toBeInTheDocument();
  expect(screen.getByText(en.taskDetail.runtime.hint)).toBeInTheDocument();
});

it("shows a live worker phase as an execution signal, not a completion claim", () => {
  const running = {
    ...task,
    runtime: {
      ...task.runtime,
      worker_progress: [{ intent_id: 9, worker: "worker-1", phase: "model_wait", idle_seconds: 37, pending_tools: 0 }],
    },
  };
  render(
    <NextIntlClientProvider locale="ko" messages={ko}>
      <TaskRuntimeSummary task={running} />
    </NextIntlClientProvider>,
  );
  expect(screen.getByText("현재 실행 중인 Worker")).toBeInTheDocument();
  expect(screen.getByText(/worker-1 · #9 · 모델 응답 대기 · 마지막 실행 신호 37초 전/)).toBeInTheDocument();
});
