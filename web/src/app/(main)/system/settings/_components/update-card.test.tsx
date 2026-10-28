import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "@/lib/api";
import type { UpdateCheck } from "@/lib/types";

import messages from "../../../../../../messages/ko.json";
import { UpdateCard } from "./update-card";

vi.mock("@/lib/api", () => ({
  api: { checkUpdate: vi.fn(), applyUpdate: vi.fn(), rollbackUpdate: vi.fn() },
  sseUrl: vi.fn(),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

const release: UpdateCheck = {
  current: "0.1.0",
  mode: "docker",
  os: "linux",
  arch: "amd64",
  repo: "KMINGON/Artex-on-ChatGPT",
  has_backup: true,
  latest: "v0.2.0",
  comparable: true,
  has_update: true,
  asset: "artex-0.2.0-linux-amd64.zip",
  asset_available: false,
};

async function showCard(info: UpdateCheck) {
  vi.mocked(api.checkUpdate).mockResolvedValue(info);
  render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <UpdateCard />
    </NextIntlClientProvider>,
  );
  await screen.findByText(info.current);
}

it("shows Docker source rebuild commands without requiring a binary release asset", async () => {
  await showCard(release);

  expect(screen.getByText(messages.settingsPage.update.dockerDesc)).toBeVisible();
  expect(screen.getByText(/git fetch origin --tags/)).toHaveTextContent("git switch --detach v0.2.0");
  expect(screen.getByText(/git fetch origin --tags/)).toHaveTextContent(
    "ARTEX_BUILD_VERSION=0.2.0 docker compose up -d --build",
  );
  expect(screen.queryByText(/자동으로 업데이트할 수 없습니다/)).not.toBeInTheDocument();
  expect(screen.queryByText(messages.settingsPage.update.willDownloadPrefix.trim())).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "v0.2.0 버전으로 업데이트" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: messages.settingsPage.update.rollback })).not.toBeInTheDocument();
  expect(api.applyUpdate).not.toHaveBeenCalled();
  expect(api.rollbackUpdate).not.toHaveBeenCalled();
});

it.each([
  undefined,
  "v0.2.0; arbitrary-command",
])("keeps Docker rebuild guidance without inserting an unknown or unsafe tag: %s", async (latest) => {
  await showCard({ ...release, latest });

  expect(screen.getByText(messages.settingsPage.update.dockerDesc)).toBeVisible();
  expect(screen.getByText("docker compose up -d --build")).toBeVisible();
  expect(screen.queryByText(/git fetch origin --tags/)).not.toBeInTheDocument();
});

it("keeps binary update and rollback controls for standalone installations", async () => {
  await showCard({ ...release, mode: "binary", asset_available: true });

  expect(screen.getByRole("button", { name: "v0.2.0 버전으로 업데이트" })).toBeEnabled();
  expect(screen.getByRole("button", { name: messages.settingsPage.update.rollback })).toBeEnabled();
  expect(screen.getByText(release.asset ?? "")).toBeVisible();
  expect(screen.queryByText(messages.settingsPage.update.dockerDesc)).not.toBeInTheDocument();
});

it("keeps the missing binary asset warning for standalone installations", async () => {
  await showCard({ ...release, mode: "binary" });

  expect(screen.getByText(/자동으로 업데이트할 수 없습니다/)).toBeVisible();
  expect(screen.getByRole("button", { name: "v0.2.0 버전으로 업데이트" })).toBeDisabled();
});
