import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "@/lib/api";
import type { Settings } from "@/lib/types";

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
async function showPage() {
  vi.mocked(api.settings).mockResolvedValue(initial);
  const rendered = render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <SystemSettingsPage />
    </NextIntlClientProvider>,
  );
  await waitFor(() => expect(screen.getByRole("spinbutton", { name: messages.presets.browserLimit })).toBeEnabled());
  return rendered;
}
it("keeps individual settings visible and saves the browser limit without presets", async () => {
  let finish!: (value: Settings) => void;
  vi.mocked(api.setSettings).mockReturnValue(
    new Promise((resolve) => {
      finish = resolve;
    }),
  );
  await showPage();
  expect(screen.getByText(messages.settingsPage.language.title)).toBeVisible();
  expect(screen.getByText(messages.settingsPage.globalProxy.title)).toBeVisible();
  for (const name of ["low", "medium", "high"]) expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
  expect(api.presets).not.toHaveBeenCalled();
  const browsers = screen.getByRole("spinbutton", { name: messages.presets.browserLimit });
  const workers = screen.getByRole("spinbutton", { name: messages.settingsPage.workers.title });
  fireEvent.change(browsers, { target: { value: "6" } });
  const controls = browsers.parentElement;
  if (!controls) throw new Error("browser settings controls missing");
  fireEvent.click(within(controls).getByRole("button", { name: messages.settingsPage.save }));
  expect(api.setSettings).toHaveBeenCalledWith({ browser_concurrency_limit: 6 });
  expect(workers).toBeDisabled();
  finish({ ...initial, browser_concurrency_limit: 6 });
  await waitFor(() => expect(workers).toBeEnabled());
  expect(workers).toHaveValue(3);
  expect(browsers).toHaveValue(6);
  expect(api.applyResourcePreset).not.toHaveBeenCalled();
});

it("keeps settings locked until a failed save finishes refreshing", async () => {
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
  const browsers = screen.getByRole("spinbutton", { name: messages.presets.browserLimit });
  expect(browsers).toBeDisabled();
  finish(initial);
  await waitFor(() => expect(browsers).toBeEnabled());
  expect(toggle).toHaveAttribute("aria-checked", "true");
});
