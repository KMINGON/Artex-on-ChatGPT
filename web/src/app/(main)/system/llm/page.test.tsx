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
          format: "openai-responses",
          model: "returned-model-id",
          context_window_k: 128,
          api_key: "",
          base_url: "",
          streaming: true,
          max_tokens: 0,
          thinking_type: "",
          max_tokens_field: "",
          session_header_key: "",
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
});
