import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "@/lib/api";

import messages from "../../../../../../messages/ko.json";
import { ProfileRetryFields, RetryPolicyPanel, ZERO_OVERRIDE } from "./retry";

vi.mock("@/lib/api", () => ({ api: { llmRetryPolicy: vi.fn(), saveLLMRetryPolicy: vi.fn() } }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

it("exposes only the applied global retry and Worker controls", async () => {
  const policy = {
    connect: { attempts: 3, interval_ms: 0 },
    empty: { attempts: 2, interval_ms: 0 },
    breaker: { attempts: 3, interval_ms: 0 },
  };
  vi.mocked(api.llmRetryPolicy).mockResolvedValue(policy);
  vi.mocked(api.saveLLMRetryPolicy).mockResolvedValue(policy);
  render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <RetryPolicyPanel />
    </NextIntlClientProvider>,
  );
  expect(await screen.findByText("빈 Worker 턴 이어쓰기")).toBeInTheDocument();
  expect(screen.queryByText(messages.llmPage.retryLayer.stream.title)).not.toBeInTheDocument();
  expect(screen.queryByText(messages.llmPage.retryLayer.intent.title)).not.toBeInTheDocument();
  expect(document.getElementById("gl-empty-ms")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: messages.llmPage.retry.save }));
  await waitFor(() => expect(api.saveLLMRetryPolicy).toHaveBeenCalledWith(policy));
});

it("exposes connection retries as the only profile override", () => {
  render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <ProfileRetryFields value={ZERO_OVERRIDE} onChange={vi.fn()} />
    </NextIntlClientProvider>,
  );
  expect(document.getElementById("pf-connect-n")).toBeInTheDocument();
  expect(document.getElementById("pf-stream-n")).toBeNull();
  expect(document.getElementById("pf-empty-n")).toBeNull();
});
