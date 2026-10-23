import "@testing-library/jest-dom/vitest";
import { type ComponentProps, useState } from "react";

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "@/lib/api";
import type { AssetInterceptRuleInput, TaskTemplate } from "@/lib/types";

import messages from "../../messages/ko.json";
import { TaskTemplateControls, taskSecondsFromMinutes } from "./task-template-controls";

vi.mock("@/lib/api", () => ({
  api: {
    taskTemplates: vi.fn(),
    taskCategories: vi.fn(),
    createTaskTemplate: vi.fn(),
    updateTaskTemplate: vi.fn(),
    deleteTaskTemplate: vi.fn(),
  },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

const labels = messages.taskTemplate;
const rule: AssetInterceptRuleInput = {
  action: "block",
  kind: "exact_url",
  pattern: "https://example.test/delete",
  note: "삭제 제외",
  enabled: true,
};
const template: TaskTemplate = {
  id: 1,
  name: "저장된 템플릿",
  description: "대상 설명",
  goal: "검증 목표",
  category_id: null,
  intercept_rules: [],
  created_at: "",
  updated_at: "",
};
type Props = ComponentProps<typeof TaskTemplateControls>;
async function showControls(props: Partial<Props> = {}, templates = [template]) {
  vi.mocked(api.taskTemplates).mockResolvedValue(templates);
  vi.mocked(api.taskCategories).mockResolvedValue([]);
  const onApply = vi.fn();
  function Controls() {
    const [selectedID, setSelectedID] = useState<number | null>(null);
    return (
      <TaskTemplateControls
        taskName=""
        timeoutSeconds={0}
        planHeartbeatSeconds={600}
        seedFirstIntent={false}
        coverageEnabled
        description=""
        goal=""
        selectedTemplateID={selectedID}
        onSelectedTemplateIDChange={setSelectedID}
        onApply={onApply}
        {...props}
      />
    );
  }
  render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <Controls />
    </NextIntlClientProvider>,
  );
  await waitFor(() => expect(screen.getByRole("combobox", { name: labels.picker.label })).toBeEnabled());
  return onApply;
}
async function chooseTemplate() {
  fireEvent.keyDown(screen.getByRole("combobox", { name: labels.picker.label }), { key: "ArrowDown" });
  fireEvent.click(await screen.findByRole("option", { name: /저장된 템플릿/ }));
}

it("saves the actual task fields separately from the template name", async () => {
  vi.mocked(api.createTaskTemplate).mockImplementation(async (input) => ({ ...template, ...input }));
  await showControls({
    taskName: "실제 작업",
    description: "작성한 설명",
    goal: "작성한 목표",
    categoryID: 4,
    timeoutSeconds: 7200,
    planHeartbeatSeconds: 1200,
    seedFirstIntent: true,
    coverageEnabled: false,
    interceptRules: [rule],
  });
  fireEvent.click(screen.getByRole("button", { name: labels.picker.saveAs }));
  const name = await screen.findByLabelText(labels.manager.nameLabel);
  expect(name).toHaveValue("");
  expect(screen.getByLabelText(labels.manager.taskNameLabel)).toHaveValue("실제 작업");
  expect(screen.getByLabelText(labels.manager.timeoutLabel)).toHaveValue(120);
  expect(screen.getByLabelText(labels.manager.heartbeatLabel)).toHaveValue(20);
  expect(screen.getByLabelText(labels.manager.seedLabel)).toHaveValue("true");
  expect(screen.getByLabelText(labels.manager.coverageLabel)).toHaveValue("false");
  fireEvent.change(name, { target: { value: "재사용 템플릿" } });
  fireEvent.click(screen.getByRole("button", { name: labels.manager.create }));
  await waitFor(() =>
    expect(api.createTaskTemplate).toHaveBeenCalledWith({
      name: "재사용 템플릿",
      task_name: "실제 작업",
      description: "작성한 설명",
      goal: "작성한 목표",
      category_id: 4,
      timeout_seconds: 7200,
      plan_heartbeat_seconds: 1200,
      seed_first_intent: true,
      coverage_enabled: false,
      intercept_rules: [rule],
    }),
  );
});

it("keeps unspecified legacy settings when editing and supports explicitly clearing a task name", async () => {
  vi.mocked(api.updateTaskTemplate).mockImplementation(async (_, input) => ({ ...template, ...input }));
  await showControls();
  fireEvent.click(screen.getByRole("button", { name: labels.picker.manage }));
  expect(await screen.findByLabelText(labels.manager.taskNameLabel)).toBeDisabled();
  expect(screen.getByLabelText(labels.manager.timeoutLabel)).toHaveValue(null);
  expect(screen.getByLabelText(labels.manager.heartbeatLabel)).toHaveValue(null);
  expect(screen.getByLabelText(labels.manager.seedLabel)).toHaveValue("");
  expect(screen.getByLabelText(labels.manager.coverageLabel)).toHaveValue("");
  fireEvent.click(screen.getByRole("button", { name: labels.manager.saveChanges }));
  await waitFor(() =>
    expect(api.updateTaskTemplate).toHaveBeenCalledWith(
      1,
      expect.objectContaining({
        task_name: null,
        timeout_seconds: null,
        plan_heartbeat_seconds: null,
        seed_first_intent: null,
        coverage_enabled: null,
      }),
    ),
  );
  await waitFor(() => expect(screen.getByRole("button", { name: labels.manager.saveChanges })).toBeEnabled());
  fireEvent.click(screen.getByRole("checkbox", { name: labels.manager.setTaskName }));
  fireEvent.change(screen.getByLabelText(labels.manager.timeoutLabel), { target: { value: "1.9" } });
  fireEvent.change(screen.getByLabelText(labels.manager.heartbeatLabel), { target: { value: "2.7" } });
  fireEvent.change(screen.getByLabelText(labels.manager.seedLabel), { target: { value: "false" } });
  fireEvent.click(screen.getByRole("button", { name: labels.manager.saveChanges }));
  await waitFor(() =>
    expect(api.updateTaskTemplate).toHaveBeenLastCalledWith(
      1,
      expect.objectContaining({
        task_name: "",
        timeout_seconds: 114,
        plan_heartbeat_seconds: 600,
        seed_first_intent: false,
      }),
    ),
  );
});

it.each<Partial<Props>>([
  { taskName: "작성한 이름" },
  { categoryID: 4 },
  { interceptRules: [rule] },
  { timeoutSeconds: 3600 },
  { planHeartbeatSeconds: 1200 },
  { seedFirstIntent: true },
  { coverageEnabled: false },
])("confirms before replacing an edited form field: %j", async (props) => {
  const onApply = await showControls({ description: template.description, goal: template.goal, ...props }, [
    {
      ...template,
      task_name: "",
      timeout_seconds: 0,
      plan_heartbeat_seconds: 600,
      seed_first_intent: false,
      coverage_enabled: true,
    },
  ]);
  await chooseTemplate();
  expect(await screen.findByRole("alertdialog")).toBeVisible();
  expect(onApply).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: labels.apply.confirm }));
  expect(onApply).toHaveBeenCalledOnce();
});

it("confirms an edited name even before a description or goal has been entered", async () => {
  const onApply = await showControls({ taskName: "작성한 이름" }, [{ ...template, task_name: "새 이름" }]);
  await chooseTemplate();
  expect(await screen.findByRole("alertdialog")).toBeVisible();
  expect(onApply).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: labels.apply.cancel }));
  expect(onApply).not.toHaveBeenCalled();
});

it("does not treat unspecified settings as overwriting current values", async () => {
  const onApply = await showControls({
    taskName: "유지할 이름",
    description: template.description,
    goal: template.goal,
    timeoutSeconds: 3600,
    planHeartbeatSeconds: 1200,
    seedFirstIntent: true,
    coverageEnabled: false,
  });
  await chooseTemplate();
  await waitFor(() => expect(onApply).toHaveBeenCalledWith(template));
  expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
});

it("preserves second precision through template editing and task minute conversion", async () => {
  vi.mocked(api.updateTaskTemplate).mockImplementation(async (_, input) => ({ ...template, ...input }));
  await showControls({}, [{ ...template, timeout_seconds: 30, plan_heartbeat_seconds: 601 }]);
  fireEvent.click(screen.getByRole("button", { name: labels.picker.manage }));
  const timeout = await screen.findByLabelText(labels.manager.timeoutLabel);
  const heartbeat = screen.getByLabelText(labels.manager.heartbeatLabel);
  expect(timeout).toHaveValue(0.5);
  expect(heartbeat).toHaveValue(601 / 60);
  expect(timeout).toHaveAttribute("step", "any");
  expect(taskSecondsFromMinutes((timeout as HTMLInputElement).value, 0)).toBe(30);
  expect(taskSecondsFromMinutes((heartbeat as HTMLInputElement).value, 600)).toBe(601);
  for (const value of ["", "invalid", -1, Infinity]) {
    expect(taskSecondsFromMinutes(value, 0)).toBe(0);
    expect(taskSecondsFromMinutes(value, 600)).toBe(600);
  }
  fireEvent.click(screen.getByRole("button", { name: labels.manager.saveChanges }));
  await waitFor(() =>
    expect(api.updateTaskTemplate).toHaveBeenCalledWith(
      1,
      expect.objectContaining({
        timeout_seconds: 30,
        plan_heartbeat_seconds: 601,
      }),
    ),
  );
});

it("searches the new query when reopening before the previous close animation finishes", async () => {
  const original = Object.getOwnPropertyDescriptor(Element.prototype, "getAnimations");
  // JSDOM has no CSS animations; hold the exit animation open like the real popup.
  Object.defineProperty(Element.prototype, "getAnimations", {
    configurable: true,
    value: () => [{ finished: new Promise(() => undefined) }],
  });
  try {
    const extended = { ...template, name: "운영 · 확장 검증" };
    const standard = { ...template, id: 2, name: "운영 · 일반 검증" };
    const onApply = await showControls({}, [extended, standard]);
    const input = screen.getByRole("combobox", { name: labels.picker.label });
    fireEvent.input(input, { target: { value: extended.name }, inputType: "insertText" });
    fireEvent.click(await screen.findByRole("option", { name: /운영 · 확장 검증/ }));
    expect(onApply).toHaveBeenLastCalledWith(extended);
    fireEvent.input(input, { target: { value: standard.name }, inputType: "insertText" });
    const next = await screen.findByRole("option", { name: /운영 · 일반 검증/ });
    expect(screen.queryByRole("option", { name: /운영 · 확장 검증/ })).not.toBeInTheDocument();
    fireEvent.click(next);
    expect(onApply).toHaveBeenLastCalledWith(standard);
  } finally {
    cleanup();
    if (original) Object.defineProperty(Element.prototype, "getAnimations", original);
    else Reflect.deleteProperty(Element.prototype, "getAnimations");
  }
});
