import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "@/lib/api";
import type { PresetCatalog, Settings } from "@/lib/types";

import messages from "../../../../../../messages/ko.json";
import { ResourcePresets } from "./resource-presets";

vi.mock("@/lib/api", () => ({ api: { presets: vi.fn(), applyResourcePreset: vi.fn(), settings: vi.fn() } }));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
const catalog: PresetCatalog = {
  policies: [],
  resources: [
    { id: "low", tier: 1, version: 1, workers: 2, tasks: 2, browsers: 2 },
    { id: "medium", tier: 2, version: 1, workers: 5, tasks: 3, browsers: 4 },
    { id: "high", tier: 3, version: 1, workers: 10, tasks: 5, browsers: 8 },
  ],
  tools: [
    { kind: "mcp", key: "browser", min_tier: 1 },
    { kind: "setting", key: "traffic_capture", min_tier: 2 },
  ],
};
const settings = { resource_preset: "custom", workers: 3, browser_concurrency_limit: 4 } as Settings;
it("applies the chosen server preset once and waits for the saved result", async () => {
  vi.mocked(api.presets).mockResolvedValue(catalog);
  let finish!: (value: Settings) => void;
  vi.mocked(api.applyResourcePreset).mockReturnValue(
    new Promise((resolve) => {
      finish = resolve;
    }),
  );
  const onApplied = vi.fn();
  render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <ResourcePresets settings={settings} onApplied={onApplied} onAdvanced={vi.fn()} />
    </NextIntlClientProvider>,
  );
  const buttons = await screen.findAllByRole("button", { name: messages.presets.apply });
  expect(screen.getAllByText(messages.presets.tools.browser)).toHaveLength(3);
  expect(screen.getAllByText(messages.presets.tools.traffic_capture)).toHaveLength(2);
  fireEvent.click(buttons[1]);
  fireEvent.click(buttons[1]);
  expect(api.applyResourcePreset).toHaveBeenCalledTimes(1);
  expect(api.applyResourcePreset).toHaveBeenCalledWith("medium");
  expect(onApplied).not.toHaveBeenCalled();
  const applied = { ...settings, resource_preset: "medium", workers: 5 };
  finish(applied);
  await waitFor(() => expect(onApplied).toHaveBeenCalledWith(applied));
});
it("refreshes persisted values after failure and opens advanced only after custom selection succeeds", async () => {
  vi.mocked(api.presets).mockResolvedValue(catalog);
  const committed = { ...settings, resource_preset: "high", workers: 10 };
  vi.mocked(api.settings).mockResolvedValue(committed);
  vi.mocked(api.applyResourcePreset)
    .mockRejectedValueOnce(new Error("fixture failure"))
    .mockResolvedValueOnce(settings);
  const onApplied = vi.fn(),
    onAdvanced = vi.fn();
  render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <ResourcePresets settings={settings} onApplied={onApplied} onAdvanced={onAdvanced} />
    </NextIntlClientProvider>,
  );
  const button = screen.getByRole("button", { name: messages.presets.customAction });
  fireEvent.click(button);
  expect(await screen.findByRole("alert")).toHaveTextContent("fixture failure");
  expect(onAdvanced).not.toHaveBeenCalled();
  await waitFor(() => expect(onApplied).toHaveBeenCalledWith(committed));
  fireEvent.click(button);
  await waitFor(() => expect(onAdvanced).toHaveBeenCalledOnce());
  expect(onApplied).toHaveBeenCalledWith(settings);
  expect(api.applyResourcePreset).toHaveBeenLastCalledWith("custom");
});
