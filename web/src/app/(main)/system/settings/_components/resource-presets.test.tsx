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
      <ResourcePresets settings={settings} onApplied={onApplied} />
    </NextIntlClientProvider>,
  );
  await screen.findByRole("button", { name: "medium" });
  const buttons = screen.getAllByRole("button");
  expect(buttons).toHaveLength(3);
  fireEvent.click(buttons[1]);
  fireEvent.click(buttons[1]);
  expect(api.applyResourcePreset).toHaveBeenCalledTimes(1);
  expect(api.applyResourcePreset).toHaveBeenCalledWith("medium");
  expect(onApplied).not.toHaveBeenCalled();
  for (const button of buttons) expect(button).toBeDisabled();
  const applied = { ...settings, resource_preset: "medium", workers: 5 };
  finish(applied);
  await waitFor(() => expect(onApplied).toHaveBeenCalledWith(applied));
});
it("refreshes actual saved values after a runtime apply failure", async () => {
  vi.mocked(api.presets).mockResolvedValue(catalog);
  const committed = { ...settings, resource_preset: "high", workers: 10 };
  vi.mocked(api.settings).mockResolvedValue(committed);
  vi.mocked(api.applyResourcePreset).mockRejectedValueOnce(new Error("fixture failure"));
  const onApplied = vi.fn(),
    onPendingChange = vi.fn();
  render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <ResourcePresets settings={settings} onApplied={onApplied} onPendingChange={onPendingChange} />
    </NextIntlClientProvider>,
  );
  fireEvent.click(await screen.findByRole("button", { name: "high" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("fixture failure");
  await waitFor(() => expect(onApplied).toHaveBeenCalledWith(committed));
  expect(onPendingChange).toHaveBeenCalledWith(true);
  await waitFor(() => expect(onPendingChange).toHaveBeenLastCalledWith(false));
  expect(screen.getByRole("button", { name: "high" })).toBeEnabled();
});
it("blocks presets during another settings save and ignores a late unmounted result", async () => {
  vi.mocked(api.presets).mockResolvedValue(catalog);
  let finish!: (value: Settings) => void;
  vi.mocked(api.applyResourcePreset).mockReturnValue(
    new Promise((resolve) => {
      finish = resolve;
    }),
  );
  const onApplied = vi.fn();
  const { rerender, unmount } = render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <ResourcePresets settings={settings} onApplied={onApplied} disabled />
    </NextIntlClientProvider>,
  );
  const button = await screen.findByRole("button", { name: "low" });
  expect(button).toBeDisabled();
  fireEvent.click(button);
  expect(api.applyResourcePreset).not.toHaveBeenCalled();
  rerender(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <ResourcePresets settings={settings} onApplied={onApplied} />
    </NextIntlClientProvider>,
  );
  fireEvent.click(button);
  expect(api.applyResourcePreset).toHaveBeenCalledOnce();
  unmount();
  finish(settings);
  await Promise.resolve();
  expect(onApplied).not.toHaveBeenCalled();
});
