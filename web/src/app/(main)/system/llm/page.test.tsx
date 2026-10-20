import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "@/lib/api";

import messages from "../../../../../messages/ko.json";
import LLMPage from "./page";

vi.mock("@/lib/api", () => ({
  api: {
    llmProfiles: vi.fn(),
    llmPool: vi.fn(),
    chatgptStatus: vi.fn(),
    chatgptModels: vi.fn(),
    saveLLMProfile: vi.fn(),
    testLLM: vi.fn(),
  },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

describe("ChatGPT model profile", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    Element.prototype.scrollIntoView = vi.fn();
    vi.mocked(api.llmProfiles).mockResolvedValue([]);
    vi.mocked(api.llmPool).mockResolvedValue({ enabled: false, bind_fallback: false, chain: [] });
    vi.mocked(api.chatgptStatus).mockResolvedValue({
      accounts: [{ id: "account-id", email: "user@example.test", connected: true, plan_enabled: true }],
      pending: false,
    });
    vi.mocked(api.chatgptModels).mockResolvedValue([
      { id: "returned-model-id", name: "계정에서 제공한 모델", context_window: 128000 },
    ]);
    vi.mocked(api.saveLLMProfile).mockResolvedValue({ id: 9 });
  });

  afterEach(() => cleanup());

  async function openChatGPTProfile() {
    await act(async () =>
      render(
        <NextIntlClientProvider locale="ko" messages={messages}>
          <LLMPage />
        </NextIntlClientProvider>,
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: "새로 만들기" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.keyDown(within(dialog).getByRole("combobox", { name: "모델 연결 방식" }), { key: "ArrowDown" });
    fireEvent.click(await screen.findByRole("option", { name: "ChatGPT 구독 계정" }));
    return within(dialog);
  }

  it("saves a server-listed model without API credentials or unsupported request options", async () => {
    const dialog = await openChatGPTProfile();
    expect(dialog.queryByLabelText("API Key")).not.toBeInTheDocument();
    expect(dialog.queryByLabelText(messages.llmPage.profile.baseUrl.label)).not.toBeInTheDocument();
    expect(dialog.getByLabelText("모델")).toHaveAttribute("readonly");
    expect(dialog.getByRole("button", { name: "새로 만들기" })).toBeDisabled();

    fireEvent.change(dialog.getByLabelText("이름"), { target: { value: "개인 구독" } });
    fireEvent.click(dialog.getByTitle(messages.llmPage.profile.model.loadTitle));
    fireEvent.click(await screen.findByRole("button", { name: /계정에서 제공한 모델/ }));
    expect(api.chatgptModels).toHaveBeenCalledWith("account-id");
    fireEvent.click(dialog.getByRole("button", { name: "새로 만들기" }));

    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({
          name: "개인 구독",
          auth_mode: "chatgpt_plan",
          chatgpt_account_id: "account-id",
          model: "returned-model-id",
          context_window_k: 128,
          api_key: "",
          max_tokens: 0,
          pool_exclude: true,
        }),
      ),
    );
  });

  it("requires a connected eligible account before loading models or saving", async () => {
    vi.mocked(api.chatgptStatus).mockResolvedValue({
      accounts: [{ id: "account-id", email: "user@example.test", connected: false, plan_enabled: true }],
      pending: false,
    });
    const dialog = await openChatGPTProfile();
    expect(dialog.getByTitle(messages.llmPage.profile.model.loadTitle)).toBeDisabled();
    expect(dialog.getByRole("button", { name: "새로 만들기" })).toBeDisabled();
    expect(api.chatgptModels).not.toHaveBeenCalled();
  });
  it("offers only OpenAI API settings and preserves the chosen model/output cap", async () => {
    await act(async () =>
      render(
        <NextIntlClientProvider locale="ko" messages={messages}>
          <LLMPage />
        </NextIntlClientProvider>,
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: "새로 만들기" }));
    const dialog = within(await screen.findByRole("dialog"));
    for (const label of [
      messages.llmPage.profile.baseUrl.label,
      messages.llmPage.profile.sessionHeader.label,
      messages.llmPage.profile.streaming.label,
      messages.llmPage.profile.thinkingType.label,
      messages.llmPage.profile.maxTokensField.label,
    ]) {
      expect(dialog.queryByText(label)).not.toBeInTheDocument();
    }
    fireEvent.change(dialog.getByLabelText("이름"), { target: { value: "OpenAI API" } });
    fireEvent.change(dialog.getByLabelText("모델"), { target: { value: "existing-openai-model" } });
    fireEvent.change(dialog.getByLabelText("API Key"), { target: { value: "test-api-key" } });
    fireEvent.change(dialog.getByLabelText(messages.llmPage.profile.maxTokens.label), { target: { value: "4096" } });
    fireEvent.click(dialog.getByRole("button", { name: "새로 만들기" }));
    await waitFor(() => expect(api.saveLLMProfile).toHaveBeenCalled());
    const body = vi.mocked(api.saveLLMProfile).mock.calls[0][0];
    expect(body).toEqual(
      expect.objectContaining({ auth_mode: "api_key", model: "existing-openai-model", max_tokens: 4096 }),
    );
    for (const key of ["format", "base_url", "thinking_type", "streaming", "max_tokens_field", "session_header_key"])
      expect(body).not.toHaveProperty(key);
  });

  it("keeps a retired profile visible and requires an explicit replacement model", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([
      {
        id: "17",
        name: "이전 연결",
        model: "legacy-model",
        auth_mode: "api_key",
        configuration_error: "OpenAI 모델과 인증을 다시 선택해 주세요",
        rate_per_second: 0,
        rate_per_minute: 0,
        is_default: false,
      },
    ]);
    await act(async () =>
      render(
        <NextIntlClientProvider locale="ko" messages={messages}>
          <LLMPage />
        </NextIntlClientProvider>,
      ),
    );
    expect(screen.getByText("legacy-model")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /이전 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByRole("alert")).toHaveTextContent("OpenAI 모델과 인증을 다시 선택해 주세요");
    expect(dialog.getByLabelText("모델")).toHaveValue("");
    expect(api.saveLLMProfile).not.toHaveBeenCalled();
  });
});
