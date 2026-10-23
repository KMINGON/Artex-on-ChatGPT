import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "@/lib/api";
import type { PresetCatalog, Settings } from "@/lib/types";

import messages from "../../../../../messages/ko.json";
import SystemSettingsPage from "./page";

vi.mock("@/lib/api", () => ({
  api: { settings: vi.fn(), presets: vi.fn(), applyResourcePreset: vi.fn(), setSettings: vi.fn() },
}));
vi.mock("./_components/update-card", () => ({ UpdateCard: () => null }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

const initial: Settings = {
  resource_preset: "custom",
  workers: 3,
  browser_concurrency_limit: 4,
  traffic_capture: false,
  agent_traffic_binding: false,
  llm_record: false,
  web_search_enabled: true,
  web_search_backend: "brave-free",
  brave_key_set: false,
  tavily_key_set: false,
  global_proxy: "http://saved.invalid:8080",
  web_search_proxy: "http://search.invalid:8080",
  python_interpreter: "python3",
};
const catalog: PresetCatalog = {
  tools: [],
  resources: [
    { id: "low", tier: 1, version: 1, workers: 2, tasks: 2, browsers: 2 },
    { id: "medium", tier: 2, version: 1, workers: 5, tasks: 3, browsers: 4 },
    { id: "high", tier: 3, version: 1, workers: 10, tasks: 5, browsers: 8 },
  ],
};
async function showPage() {
  vi.mocked(api.settings).mockResolvedValue(initial);
  vi.mocked(api.presets).mockResolvedValue(catalog);
  const rendered = render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <SystemSettingsPage />
    </NextIntlClientProvider>,
  );
  await waitFor(() => expect(screen.getByRole("button", { name: "medium" })).toBeEnabled());
  return rendered;
}
it("keeps all detail cards visible and updates preset values without replacing other drafts", async () => {
  let finish!: (value: Settings) => void;
  vi.mocked(api.applyResourcePreset).mockReturnValue(
    new Promise((resolve) => {
      finish = resolve;
    }),
  );
  const { container } = await showPage();
  expect(screen.queryByRole("tab")).not.toBeInTheDocument();
  expect(screen.getByText(messages.settingsPage.language.title)).toBeVisible();
  expect(screen.getByText(messages.settingsPage.globalProxy.title)).toBeVisible();
  const workers = screen.getByRole("spinbutton", { name: messages.settingsPage.workers.title });
  const browsers = screen.getByRole("spinbutton", { name: messages.presets.browserLimit });
  const globalProxy = screen.getByLabelText(messages.settingsPage.globalProxy.addrLabel);
  const searchProxy = screen.getByLabelText(messages.settingsPage.webSearch.proxyLabel);
  const key = screen.getByLabelText(messages.settingsPage.webSearch.braveKeyLabel);
  const python = screen.getByPlaceholderText(messages.settingsPage.python.placeholder);
  for (const [input, value] of [
    [globalProxy, "http://draft.invalid:8080"],
    [searchProxy, "http://draft-search.invalid:8080"],
    [key, "test-only-draft"],
    [python, "/draft/python"],
    [workers, "12"],
    [browsers, "9"],
  ] as const) {
    fireEvent.change(input, { target: { value } });
  }
  fireEvent.click(screen.getByRole("button", { name: "medium" }));
  expect(workers).toBeDisabled();
  expect(globalProxy).toBeDisabled();
  finish({
    ...initial,
    resource_preset: "medium",
    workers: 5,
    browser_concurrency_limit: 4,
    task_concurrency_enabled: true,
    task_concurrency_limit: 3,
    traffic_capture: true,
    agent_traffic_binding: true,
  });
  await waitFor(() => expect(workers).toHaveValue(5));
  expect(browsers).toHaveValue(4);
  expect(container.querySelector("#traffic-capture")).toHaveAttribute("aria-checked", "true");
  expect(container.querySelector("#agent-traffic-binding")).toHaveAttribute("aria-checked", "true");
  expect(globalProxy).toHaveValue("http://draft.invalid:8080");
  expect(searchProxy).toHaveValue("http://draft-search.invalid:8080");
  expect(key).toHaveValue("test-only-draft");
  expect(python).toHaveValue("/draft/python");
  expect(screen.getByRole("button", { name: "medium" })).toHaveAttribute("aria-pressed", "true");
  expect(api.setSettings).not.toHaveBeenCalled();
  await waitFor(() => expect(workers).toBeEnabled());
});
it("disables preset application until an individual setting save completes", async () => {
  let finish!: (value: Settings) => void;
  vi.mocked(api.setSettings).mockReturnValue(
    new Promise((resolve) => {
      finish = resolve;
    }),
  );
  await showPage();
  const browsers = screen.getByRole("spinbutton", { name: messages.presets.browserLimit });
  fireEvent.change(browsers, { target: { value: "6" } });
  const controls = browsers.parentElement;
  if (!controls) throw new Error("browser settings controls missing");
  fireEvent.click(within(controls).getByRole("button", { name: messages.settingsPage.save }));
  for (const name of ["low", "medium", "high"]) expect(screen.getByRole("button", { name })).toBeDisabled();
  finish({ ...initial, browser_concurrency_limit: 6 });
  await waitFor(() => expect(screen.getByRole("button", { name: "high" })).toBeEnabled());
  expect(browsers).toHaveValue(6);
});

it("keeps presets locked until a failed settings save finishes refreshing", async () => {
  const { container } = await showPage();
  let finish!: (value: Settings) => void;
  vi.mocked(api.settings).mockReturnValueOnce(
    new Promise((resolve) => {
      finish = resolve;
    }),
  );
  vi.mocked(api.setSettings).mockRejectedValueOnce(new Error("fixture failure"));
  const toggle = container.querySelector("#web-search");
  if (!toggle) throw new Error("web search switch missing");
  fireEvent.click(toggle);
  await waitFor(() => expect(api.settings).toHaveBeenCalledTimes(2));
  const medium = screen.getByRole("button", { name: "medium" });
  expect(medium).toBeDisabled();
  fireEvent.click(medium);
  expect(api.applyResourcePreset).not.toHaveBeenCalled();
  finish(initial);
  await waitFor(() => expect(medium).toBeEnabled());
  expect(toggle).toHaveAttribute("aria-checked", "true");
});
