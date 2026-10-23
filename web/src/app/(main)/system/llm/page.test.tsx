import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "@/lib/api";
import type { ChatGPTModel, LLMProfile, ModelCapabilities } from "@/lib/types";

import englishMessages from "../../../../../messages/en.json";
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
    activateLLMProfile: vi.fn(),
    fetchLLMModels: vi.fn(),
  },
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const account = { id: "account-id", email: "user@example.test", connected: true, plan_enabled: true };
const catalogCapabilities: ModelCapabilities = {
  known: true,
  reasoning_efforts_known: true,
  access_programs_known: true,
  reasoning_efforts: ["none", "low", "medium", "high"],
  default_reasoning_effort: "medium",
  access_programs_cyber: ["standard", "daybreak_blue"],
  daybreak_available: true,
  source: "account_catalog",
};
const subscriptionModel: ChatGPTModel = {
  id: "gpt-6-sol",
  name: "GPT-6 Sol",
  context_window: 272000,
  capabilities: catalogCapabilities,
};

function savedProfile(patch: Partial<LLMProfile> = {}): LLMProfile {
  return {
    id: "19",
    name: "저장된 연결",
    model: "gpt-6-sol",
    auth_mode: "api_key",
    connected: true,
    api_key_hint: "sk-…saved",
    rate_per_second: 0,
    rate_per_minute: 0,
    is_default: false,
    ...patch,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function showPage() {
  await act(async () => {
    render(
      <NextIntlClientProvider locale="ko" messages={messages}>
        <LLMPage />
      </NextIntlClientProvider>,
    );
  });
}

async function openNewProfile() {
  await showPage();
  fireEvent.click(screen.getByRole("button", { name: "모델 설정 추가" }));
  return within(await screen.findByRole("dialog"));
}

async function choose(trigger: HTMLElement, name: string | RegExp) {
  fireEvent.keyDown(trigger, { key: "ArrowDown" });
  fireEvent.click(await screen.findByRole("option", { name }));
}

async function loadSubscriptionModel(dialog: ReturnType<typeof within>, name = /GPT-6 Sol/) {
  fireEvent.click(dialog.getByRole("button", { name: "계정 모델 조회" }));
  const select = await dialog.findByRole("combobox", { name: "모델" });
  await choose(select, name);
}

async function checkConnection(dialog: ReturnType<typeof within>, button = "연결 확인") {
  fireEvent.click(dialog.getByRole("button", { name: button }));
  const result = await dialog.findByRole("region", { name: "최근 연결 확인" });
  expect(result).toHaveTextContent("연결됨");
  await waitFor(() => expect(dialog.getByRole("button", { name: "저장" })).toBeEnabled());
  return result;
}

async function openAPIProfile() {
  vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [], pending: false });
  const dialog = await openNewProfile();
  fireEvent.change(dialog.getByLabelText("API Key"), { target: { value: "test-api-key" } });
  fireEvent.click(dialog.getByRole("button", { name: "API 모델 조회" }));
  await choose(await dialog.findByRole("combobox", { name: "모델" }), /GPT-6 Sol/);
  return dialog;
}

function expandAdvanced(dialog: ReturnType<typeof within>) {
  const summary = dialog.getByText("고급 설정", { selector: "summary" });
  fireEvent.click(summary);
}

describe("OpenAI model profiles", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    Element.prototype.scrollIntoView = vi.fn();
    vi.mocked(api.llmProfiles).mockResolvedValue([]);
    vi.mocked(api.llmPool).mockResolvedValue({ enabled: false, bind_fallback: false, chain: [] });
    vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [account], pending: false });
    vi.mocked(api.chatgptModels).mockResolvedValue([subscriptionModel]);
    vi.mocked(api.fetchLLMModels).mockResolvedValue({
      ok: true,
      model_details: [{ ...subscriptionModel, capabilities: { ...catalogCapabilities, source: "documentation" } }],
    });
    vi.mocked(api.saveLLMProfile).mockResolvedValue({ id: 9 });
    vi.mocked(api.activateLLMProfile).mockResolvedValue({ ok: true });
    vi.mocked(api.testLLM).mockResolvedValue({
      ok: true,
      model: "gpt-6-sol",
      actual_model: "gpt-6-sol",
      actual_access_program_cyber: "daybreak_blue",
      access_programs_present: true,
      latency_ms: 1200,
      reply: "OK",
    });
  });

  afterEach(() => cleanup());

  it("switches the fork account and model controls to English without losing the open editor", async () => {
    const view = render(
      <NextIntlClientProvider locale="ko" messages={messages}>
        <LLMPage />
      </NextIntlClientProvider>,
    );
    await screen.findByText("ChatGPT 계정 연결");
    fireEvent.click(screen.getByRole("button", { name: "모델 설정 추가" }));
    await screen.findByRole("dialog");
    await act(async () =>
      view.rerender(
        <NextIntlClientProvider locale="en" messages={englishMessages}>
          <LLMPage />
        </NextIntlClientProvider>,
      ),
    );
    expect(screen.getByText("ChatGPT accounts")).toBeInTheDocument();
    const dialog = within(screen.getByRole("dialog"));
    expect(dialog.getByRole("button", { name: "Test connection" })).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(dialog.getByText("Advanced settings", { selector: "summary" })).toBeInTheDocument();
    expect(dialog.queryByText("연결 확인")).not.toBeInTheDocument();
  });

  it("keeps account login separate and adds model profiles from inside the models tab", async () => {
    await showPage();
    expect(screen.getByText("ChatGPT 계정 연결")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "ChatGPT로 계속" })).toBeInTheDocument();
    const panel = screen.getByRole("tabpanel", { name: messages.llmPage.page.tab.profiles });
    expect(within(panel).getByRole("button", { name: "모델 설정 추가" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: messages.llmPage.page.poolConfig })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "새로 만들기" })).not.toBeInTheDocument();
  });

  it("prefills the sole connected account and loads models only when explicitly requested", async () => {
    const dialog = await openNewProfile();
    expect(dialog.getByRole("combobox", { name: "모델 연결 방식" })).toHaveTextContent("ChatGPT 구독 계정");
    expect(dialog.getByRole("combobox", { name: "ChatGPT 계정" })).toHaveTextContent(account.email);
    expect(dialog.queryByLabelText("API Key")).not.toBeInTheDocument();
    expect(dialog.queryByRole("combobox", { name: "모델" })).not.toBeInTheDocument();
    expect(dialog.queryByRole("switch", { name: "Daybreak 자동" })).not.toBeInTheDocument();
    expect(api.chatgptModels).not.toHaveBeenCalled();

    await loadSubscriptionModel(dialog);
    expect(api.chatgptModels).toHaveBeenCalledExactlyOnceWith("account-id");
    expect(dialog.queryByRole("switch", { name: "Daybreak 자동" })).not.toBeInTheDocument();
    expect(dialog.getByRole("combobox", { name: "모델" })).toHaveTextContent("Daybreak");
    expect((dialog.getByLabelText("이름") as HTMLInputElement).value).toContain("GPT-6 Sol");
    await choose(dialog.getByRole("combobox", { name: "모델" }), /GPT-6 Sol/);
    expect(api.chatgptModels).toHaveBeenCalledTimes(1);
    expect(api.testLLM).not.toHaveBeenCalled();
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    expect(dialog.getByRole("button", { name: "저장하고 사용" })).toBeDisabled();
    await checkConnection(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({
          auth_mode: "chatgpt_plan",
          chatgpt_account_id: "account-id",
          model: "gpt-6-sol",
          context_window_k: 272,
          access_program_cyber: "",
          api_key: "",
          max_tokens: 0,
          pool_exclude: true,
        }),
      ),
    );
  });

  it("defaults to automatic Daybreak and preserves a manually named subscription profile", async () => {
    const dialog = await openNewProfile();
    fireEvent.change(dialog.getByLabelText("이름"), { target: { value: "개인 구독" } });
    await loadSubscriptionModel(dialog);
    expect(dialog.getByLabelText("이름")).toHaveValue("개인 구독");
    await checkConnection(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "저장하고 사용" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({ name: "개인 구독", access_program_cyber: "", auth_mode: "chatgpt_plan" }),
      ),
    );
    await waitFor(() => expect(api.activateLLMProfile).toHaveBeenCalledWith("9"));
  });

  it("preserves an existing subscription Standard selection until the user chooses automatic selection", async () => {
    const otherAccount = { ...account, id: "other-account", email: "other@example.test" };
    vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [account, otherAccount], pending: false });
    vi.mocked(api.chatgptModels).mockResolvedValue([
      subscriptionModel,
      { ...subscriptionModel, id: "gpt-6-luna", name: "GPT-6 Luna" },
    ]);
    vi.mocked(api.llmProfiles).mockResolvedValue([
      savedProfile({
        auth_mode: "chatgpt_plan",
        chatgpt_account_id: account.id,
        access_program_cyber: "standard",
        capabilities: catalogCapabilities,
      }),
    ]);
    vi.mocked(api.testLLM).mockResolvedValueOnce({
      ok: true,
      actual_model: "gpt-6-sol",
      actual_access_program_cyber: "standard",
      access_programs_present: true,
    });
    await showPage();
    fireEvent.click(screen.getByRole("button", { name: /저장된 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.queryByRole("switch", { name: "Daybreak 자동" })).not.toBeInTheDocument();
    await checkConnection(dialog);
    expect(api.testLLM).toHaveBeenLastCalledWith(expect.objectContaining({ access_program_cyber: "standard" }));
    fireEvent.click(dialog.getByRole("button", { name: "모델 목록 새로고침" }));
    await waitFor(() => expect(api.chatgptModels).toHaveBeenCalled());
    await choose(dialog.getByRole("combobox", { name: "모델" }), /GPT-6 Luna/);
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    vi.mocked(api.testLLM).mockResolvedValueOnce({
      ok: true,
      actual_model: "gpt-6-luna",
      actual_access_program_cyber: "standard",
      access_programs_present: true,
    });
    await checkConnection(dialog);
    expect(api.testLLM).toHaveBeenLastCalledWith(
      expect.objectContaining({ model: "gpt-6-luna", access_program_cyber: "standard" }),
    );
    await choose(dialog.getByRole("combobox", { name: "ChatGPT 계정" }), /other@example.test/);
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    await loadSubscriptionModel(dialog, /GPT-6 Luna/);
    vi.mocked(api.testLLM).mockResolvedValueOnce({
      ok: true,
      actual_model: "gpt-6-luna",
      actual_access_program_cyber: "standard",
      access_programs_present: true,
    });
    await checkConnection(dialog);
    expect(api.testLLM).toHaveBeenLastCalledWith(
      expect.objectContaining({ chatgpt_account_id: otherAccount.id, access_program_cyber: "standard" }),
    );
    fireEvent.click(dialog.getByRole("button", { name: "자동 선택으로 변경" }));
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    vi.mocked(api.testLLM).mockResolvedValueOnce({
      ok: true,
      actual_model: "gpt-6-luna",
      actual_access_program_cyber: "daybreak_blue",
      access_programs_present: true,
    });
    await checkConnection(dialog);
    expect(api.testLLM).toHaveBeenLastCalledWith(
      expect.objectContaining({ model: "gpt-6-luna", access_program_cyber: "" }),
    );
  });

  it.each([
    { programs: ["standard"], expected: "standard", label: "Standard", blue: false },
    { programs: ["daybreak_blue"], expected: "", label: "Daybreak", blue: true },
  ])("uses only the account catalog choices: $programs", async ({ programs, expected, label, blue }) => {
    vi.mocked(api.chatgptModels).mockResolvedValue([
      {
        ...subscriptionModel,
        capabilities: { ...catalogCapabilities, access_programs_cyber: programs, daybreak_available: blue },
      },
    ]);
    const dialog = await openNewProfile();
    await loadSubscriptionModel(dialog);
    expect(dialog.queryByRole("switch", { name: "Daybreak 자동" })).not.toBeInTheDocument();
    expect(dialog.getByRole("combobox", { name: "모델" })).toHaveTextContent(label);
    vi.mocked(api.testLLM).mockResolvedValueOnce({
      ok: true,
      actual_model: "gpt-6-sol",
      actual_access_program_cyber: blue ? "daybreak_blue" : "standard",
      access_programs_present: true,
    });
    await checkConnection(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(expect.objectContaining({ access_program_cyber: expected })),
    );
  });

  it("does not infer Daybreak eligibility from the model name when the catalog omits program metadata", async () => {
    vi.mocked(api.chatgptModels).mockResolvedValue([
      {
        id: "gpt-daybreak-blue-latest",
        name: "Daybreak Blue Latest",
        capabilities: {
          known: false,
          access_programs_known: false,
          reasoning_efforts_known: false,
          daybreak_available: null,
          source: "unknown",
        },
      },
    ]);
    const dialog = await openNewProfile();
    await loadSubscriptionModel(dialog, /Daybreak Blue Latest/);
    expect(dialog.getByRole("combobox", { name: "모델" })).toHaveTextContent("미확인");
    expect(dialog.queryByText("Daybreak 자동", { exact: true })).not.toBeInTheDocument();
    expect(dialog.queryByRole("switch", { name: "Daybreak 자동" })).not.toBeInTheDocument();
  });

  it("ignores a late catalog response after switching subscription accounts", async () => {
    const otherAccount = { ...account, id: "other-account", email: "other@example.test" };
    vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [account, otherAccount], pending: false });
    const pending = deferred<ChatGPTModel[]>();
    vi.mocked(api.chatgptModels).mockReturnValueOnce(pending.promise);
    const dialog = await openNewProfile();
    await choose(dialog.getByRole("combobox", { name: "모델 연결 방식" }), "ChatGPT 구독 계정");
    await choose(dialog.getByRole("combobox", { name: "ChatGPT 계정" }), new RegExp(account.email));
    fireEvent.click(dialog.getByRole("button", { name: "계정 모델 조회" }));
    await choose(dialog.getByRole("combobox", { name: "ChatGPT 계정" }), new RegExp(otherAccount.email));
    await act(async () => pending.resolve([subscriptionModel]));
    expect(dialog.queryByRole("combobox", { name: "모델" })).not.toBeInTheDocument();
    expect(dialog.queryByRole("switch", { name: "Daybreak 자동" })).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "계정 모델 조회" })).toBeEnabled();
  });

  it("requires an eligible account before subscription lookup or saving", async () => {
    vi.mocked(api.chatgptStatus).mockResolvedValue({
      accounts: [{ ...account, connected: false }],
      pending: false,
    });
    const dialog = await openNewProfile();
    await choose(dialog.getByRole("combobox", { name: "모델 연결 방식" }), "ChatGPT 구독 계정");
    expect(dialog.getByRole("button", { name: "계정 모델 조회" })).toBeDisabled();
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    expect(api.chatgptModels).not.toHaveBeenCalled();
  });

  it("requires an API key before lookup and saves the selected model with API-only output settings", async () => {
    vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [], pending: false });
    const dialog = await openNewProfile();
    expect(dialog.getByRole("button", { name: "API 모델 조회" })).toBeDisabled();
    expect(dialog.queryByRole("combobox", { name: "모델" })).not.toBeInTheDocument();
    fireEvent.change(dialog.getByLabelText("API Key"), { target: { value: "test-api-key" } });
    fireEvent.click(dialog.getByRole("button", { name: "API 모델 조회" }));
    await choose(await dialog.findByRole("combobox", { name: "모델" }), /GPT-6 Sol/);
    expect(api.fetchLLMModels).toHaveBeenCalledExactlyOnceWith("test-api-key", "", undefined);
    expandAdvanced(dialog);
    fireEvent.change(dialog.getByLabelText(messages.llmPage.profile.maxTokens.label), { target: { value: "4096" } });
    await checkConnection(dialog);
    expect(api.testLLM).toHaveBeenCalledWith(expect.objectContaining({ max_tokens: 4096 }));
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() => expect(api.saveLLMProfile).toHaveBeenCalled());
    const body = vi.mocked(api.saveLLMProfile).mock.calls[0][0];
    expect(body).toEqual(expect.objectContaining({ auth_mode: "api_key", model: "gpt-6-sol", max_tokens: 4096 }));
    for (const key of ["format", "base_url", "thinking_type", "streaming", "max_tokens_field", "session_header_key"])
      expect(body).not.toHaveProperty(key);
  });

  it("uses a saved API credential for catalog lookup without asking for the key again", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([savedProfile()]);
    await showPage();
    fireEvent.click(screen.getByRole("button", { name: /저장된 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByLabelText("API Key")).toHaveValue("");
    expect(dialog.getByRole("combobox", { name: "모델" })).toHaveTextContent("gpt-6-sol");
    fireEvent.click(dialog.getByRole("button", { name: "모델 목록 새로고침" }));
    await waitFor(() => expect(api.fetchLLMModels).toHaveBeenCalledExactlyOnceWith("", "", 19));
  });

  it("discards an API catalog response if its key was replaced while loading", async () => {
    vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [], pending: false });
    const pending = deferred<Awaited<ReturnType<typeof api.fetchLLMModels>>>();
    vi.mocked(api.fetchLLMModels).mockReturnValueOnce(pending.promise);
    const dialog = await openNewProfile();
    fireEvent.change(dialog.getByLabelText("API Key"), { target: { value: "first-test-key" } });
    fireEvent.click(dialog.getByRole("button", { name: "API 모델 조회" }));
    fireEvent.change(dialog.getByLabelText("API Key"), { target: { value: "second-test-key" } });
    await act(async () => pending.resolve({ ok: true, model_details: [subscriptionModel] }));
    expect(dialog.queryByRole("combobox", { name: "모델" })).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "API 모델 조회" })).toBeEnabled();
  });

  it.each([
    "연결 확인",
    "Daybreak 확인",
  ])("allows saving after %s without forcing a Daybreak program", async (button) => {
    const dialog = await openAPIProfile();
    expect(dialog.queryByRole("button", { name: "API 모델 조회" })).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Daybreak 확인" })).toBeEnabled();
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    expect(dialog.getByRole("button", { name: "저장하고 사용" })).toBeDisabled();
    await checkConnection(dialog, button);
    expect(api.testLLM).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ auth_mode: "api_key", model: "gpt-6-sol", access_program_cyber: "" }),
    );
    expect(dialog.getByRole("button", { name: "저장하고 사용" })).toBeEnabled();
    fireEvent.change(dialog.getByLabelText("이름"), { target: { value: "검증된 API" } });
    expect(dialog.getByRole("button", { name: "저장" })).toBeEnabled();
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({
          name: "검증된 API",
          access_program_cyber: "",
          connection_check: {
            model: "gpt-6-sol",
            access_program_cyber: "daybreak_blue",
            checked_at: expect.any(String),
          },
        }),
      ),
    );
  });

  it("keeps a successful API Standard result without retrying an explicit Daybreak request", async () => {
    vi.mocked(api.testLLM).mockResolvedValueOnce({
      ok: true,
      actual_model: "gpt-6-sol",
      actual_access_program_cyber: "standard",
      access_programs_present: true,
    });
    const dialog = await openAPIProfile();
    const result = await checkConnection(dialog, "Daybreak 확인");
    expect(result).toHaveTextContent("gpt-6-sol · Standard · 연결됨");
    expect(api.testLLM).toHaveBeenCalledTimes(1);
    expect(api.testLLM).toHaveBeenCalledWith(expect.objectContaining({ access_program_cyber: "" }));
  });

  it.each([
    "API Key",
    "model",
    "proxy",
    "effort",
    "program",
    "max_tokens",
  ])("invalidates the successful connection and save permission after changing %s", async (field) => {
    vi.mocked(api.fetchLLMModels).mockResolvedValue({
      ok: true,
      model_details: [subscriptionModel, { ...subscriptionModel, id: "gpt-6-luna", name: "GPT-6 Luna" }],
    });
    const dialog = await openAPIProfile();
    await checkConnection(dialog);
    expandAdvanced(dialog);
    if (field === "API Key")
      fireEvent.change(dialog.getByLabelText("API Key"), { target: { value: "replacement-test-key" } });
    else if (field === "model") await choose(dialog.getByRole("combobox", { name: "모델" }), /GPT-6 Luna/);
    else if (field === "proxy")
      fireEvent.change(dialog.getByLabelText(messages.llmPage.profile.proxy.label), {
        target: { value: "http://proxy.example.test:8080" },
      });
    else if (field === "effort")
      await choose(dialog.getByRole("combobox", { name: messages.llmPage.profile.effort.label }), "high");
    else if (field === "program")
      await choose(dialog.getByRole("combobox", { name: "API 액세스 프로그램" }), "standard");
    else
      fireEvent.change(dialog.getByLabelText(messages.llmPage.profile.maxTokens.label), {
        target: { value: "4096" },
      });
    expect(dialog.queryByRole("region", { name: "최근 연결 확인" })).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    expect(dialog.getByRole("button", { name: "저장하고 사용" })).toBeDisabled();
  });

  it("relocks saving when the same configuration is tested again and fails", async () => {
    const dialog = await openAPIProfile();
    await checkConnection(dialog);
    const pending = deferred<Awaited<ReturnType<typeof api.testLLM>>>();
    vi.mocked(api.testLLM).mockReturnValueOnce(pending.promise);
    fireEvent.click(dialog.getByRole("button", { name: "연결 확인" }));
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    await act(async () => pending.resolve({ ok: false, error: "사용 한도를 확인해 주세요." }));
    const result = await dialog.findByRole("region", { name: "최근 연결 확인" });
    expect(result).toHaveTextContent("사용 한도를 확인해 주세요.");
    expect(result).not.toHaveTextContent("연결됨");
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    expect(dialog.getByRole("button", { name: "저장하고 사용" })).toBeDisabled();
    expect(api.saveLLMProfile).not.toHaveBeenCalled();
  });

  it("does not restore a previous connection after changing settings back to their old values", async () => {
    const dialog = await openAPIProfile();
    await checkConnection(dialog);
    const effortSelect = dialog.getByRole("combobox", { name: messages.llmPage.profile.effort.label });
    await choose(effortSelect, "high");
    await choose(effortSelect, messages.llmPage.profile.effort.opt.none);
    expect(dialog.queryByRole("region", { name: "최근 연결 확인" })).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
  });

  it("shows the actual subscription program rather than assuming the catalog program was applied", async () => {
    vi.mocked(api.testLLM).mockResolvedValueOnce({
      ok: true,
      actual_model: "gpt-6-sol-2026-10-23",
      actual_access_program_cyber: "standard",
      access_programs_present: true,
    });
    const dialog = await openNewProfile();
    await loadSubscriptionModel(dialog);
    expect(dialog.getByRole("combobox", { name: "모델" })).toHaveTextContent("Daybreak");
    const result = await checkConnection(dialog);
    expect(result).toHaveTextContent("gpt-6-sol-2026-10-23 · Standard · 연결됨");
    expect(dialog.getByRole("button", { name: "연결 확인" })).toHaveTextContent(
      "gpt-6-sol-2026-10-23 · Standard · 연결됨",
    );
    expect(api.testLLM).toHaveBeenCalledWith(
      expect.objectContaining({
        auth_mode: "chatgpt_plan",
        chatgpt_account_id: "account-id",
        access_program_cyber: "",
      }),
    );
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({
          connection_check: {
            model: "gpt-6-sol-2026-10-23",
            access_program_cyber: "standard",
            checked_at: expect.any(String),
          },
        }),
      ),
    );
  });

  it("does not display an in-flight test result for a changed configuration", async () => {
    const pending = deferred<Awaited<ReturnType<typeof api.testLLM>>>();
    vi.mocked(api.testLLM).mockReturnValueOnce(pending.promise);
    const dialog = await openNewProfile();
    await loadSubscriptionModel(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "연결 확인" }));
    await choose(dialog.getByRole("combobox", { name: messages.llmPage.profile.effort.label }), "high");
    await act(async () =>
      pending.resolve({
        ok: true,
        actual_model: "gpt-6-sol",
        access_programs_present: true,
        actual_access_program_cyber: "daybreak_blue",
      }),
    );
    expect(dialog.queryByRole("region", { name: "최근 연결 확인" })).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
  });

  it("preserves an unrecognized actual program returned by OpenAI instead of calling it absent", async () => {
    vi.mocked(api.testLLM).mockResolvedValue({
      ok: true,
      actual_model: "gpt-6-sol",
      access_programs_present: true,
      actual_access_program_cyber: "future_program",
      reply: "OK",
    });
    const dialog = await openNewProfile();
    await loadSubscriptionModel(dialog);
    const result = await checkConnection(dialog);
    expect(result).toHaveTextContent("future_program");
    expect(result).not.toHaveTextContent("명시 없음");
  });

  it("does not label a successful response as Standard when OpenAI omits program metadata", async () => {
    vi.mocked(api.testLLM).mockResolvedValueOnce({ ok: true, actual_model: "gpt-6-sol" });
    const dialog = await openAPIProfile();
    const result = await checkConnection(dialog);
    expect(result).toHaveTextContent("프로그램 미확인");
    expect(result).not.toHaveTextContent("Standard");
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({
          connection_check: expect.objectContaining({ access_program_cyber: "" }),
        }),
      ),
    );
  });

  it("uses the same verified program badges on API and subscription cards", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([
      savedProfile({
        id: "1",
        name: "API Daybreak",
        connection_check: {
          model: "gpt-6-sol",
          access_program_cyber: "daybreak_blue",
          checked_at: "2026-10-23T00:00:00Z",
        },
      }),
      savedProfile({
        id: "2",
        name: "구독 Standard",
        auth_mode: "chatgpt_plan",
        chatgpt_account_id: account.id,
        capabilities: catalogCapabilities,
        connection_check: {
          model: "gpt-6-sol",
          access_program_cyber: "standard",
          checked_at: "2026-10-23T00:00:00Z",
        },
      }),
      savedProfile({ id: "3", name: "이전 API 설정", access_program_cyber: "daybreak_blue" }),
    ]);
    await showPage();
    expect(within(screen.getByRole("button", { name: /API Daybreak/ })).getByText("Daybreak Blue")).toBeInTheDocument();
    expect(within(screen.getByRole("button", { name: /구독 Standard/ })).getByText("Standard")).toBeInTheDocument();
    expect(
      within(screen.getByRole("button", { name: /이전 API 설정/ })).getByText("프로그램 미확인"),
    ).toBeInTheDocument();
  });

  it("requires a fresh check when editing a profile with a stored successful check", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([
      savedProfile({
        connection_check: {
          model: "gpt-6-sol",
          access_program_cyber: "daybreak_blue",
          checked_at: "2026-10-23T00:00:00Z",
        },
      }),
    ]);
    await showPage();
    fireEvent.click(screen.getByRole("button", { name: /저장된 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    expect(dialog.getByRole("button", { name: "저장하고 사용" })).toBeDisabled();
    await checkConnection(dialog);
  });

  it("preserves explicit none effort and the API program selector on an existing profile", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([
      savedProfile({ capabilities: { ...catalogCapabilities, source: "documentation" } }),
    ]);
    await showPage();
    fireEvent.click(screen.getByRole("button", { name: /저장된 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    await choose(dialog.getByRole("combobox", { name: messages.llmPage.profile.effort.label }), "none");
    expandAdvanced(dialog);
    await choose(dialog.getByRole("combobox", { name: "API 액세스 프로그램" }), "daybreak_blue");
    await checkConnection(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({
          id: 19,
          model: "gpt-6-sol",
          reasoning_effort: "none",
          access_program_cyber: "daybreak_blue",
          api_key: "",
        }),
      ),
    );
  });

  it("preserves an explicit API program when selecting a different supported model", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([savedProfile({ access_program_cyber: "daybreak_blue" })]);
    vi.mocked(api.fetchLLMModels).mockResolvedValue({
      ok: true,
      model_details: [
        {
          id: "gpt-6.1-sol",
          name: "GPT-6.1 Sol",
          capabilities: { ...catalogCapabilities, source: "documentation" },
        },
      ],
    });
    await showPage();
    fireEvent.click(screen.getByRole("button", { name: /저장된 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.click(dialog.getByRole("button", { name: "모델 목록 새로고침" }));
    await waitFor(() => expect(api.fetchLLMModels).toHaveBeenCalled());
    await choose(dialog.getByRole("combobox", { name: "모델" }), "GPT-6.1 Sol");
    vi.mocked(api.testLLM).mockResolvedValueOnce({
      ok: true,
      actual_model: "gpt-6.1-sol",
      actual_access_program_cyber: "daybreak_blue",
      access_programs_present: true,
    });
    await checkConnection(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({ model: "gpt-6.1-sol", access_program_cyber: "daybreak_blue" }),
      ),
    );
  });

  it("aligns Standard-only catalog display with the saved subscription request after refreshing an old profile", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([
      savedProfile({ auth_mode: "chatgpt_plan", chatgpt_account_id: account.id, api_key_hint: undefined }),
    ]);
    vi.mocked(api.chatgptModels).mockResolvedValue([
      {
        ...subscriptionModel,
        capabilities: {
          ...catalogCapabilities,
          access_programs_cyber: ["standard"],
          daybreak_available: false,
        },
      },
    ]);
    await showPage();
    fireEvent.click(screen.getByRole("button", { name: /저장된 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.click(dialog.getByRole("button", { name: "모델 목록 새로고침" }));
    await waitFor(() => expect(dialog.getByRole("combobox", { name: "모델" })).toHaveTextContent("Standard"));
    vi.mocked(api.testLLM).mockResolvedValueOnce({
      ok: true,
      actual_model: "gpt-6-sol",
      actual_access_program_cyber: "standard",
      access_programs_present: true,
    });
    await checkConnection(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({ auth_mode: "chatgpt_plan", access_program_cyber: "standard" }),
      ),
    );
  });

  it("treats documentation gaps as advisory and preserves explicit API options", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([
      savedProfile({
        reasoning_effort: "ultra",
        access_program_cyber: "daybreak_red",
        capabilities: { ...catalogCapabilities, source: "documentation" },
      }),
    ]);
    await showPage();
    fireEvent.click(screen.getByRole("button", { name: /저장된 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByRole("button", { name: "연결 확인" })).toBeEnabled();
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    await checkConnection(dialog);
    await waitFor(() =>
      expect(api.testLLM).toHaveBeenCalledWith(
        expect.objectContaining({ reasoning_effort: "ultra", access_program_cyber: "daybreak_red" }),
      ),
    );
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({ reasoning_effort: "ultra", access_program_cyber: "daybreak_red" }),
      ),
    );
  });

  it("keeps an unknown saved model and effort editable without inferring capabilities", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([savedProfile({ model: "future-model", reasoning_effort: "ultra" })]);
    await showPage();
    fireEvent.click(screen.getByRole("button", { name: /저장된 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByRole("combobox", { name: "모델" })).toHaveTextContent("future-model");
    expect(dialog.getByRole("combobox", { name: messages.llmPage.profile.effort.label })).toHaveTextContent("ultra");
    await checkConnection(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api.saveLLMProfile).toHaveBeenCalledWith(
        expect.objectContaining({ model: "future-model", reasoning_effort: "ultra" }),
      ),
    );
  });

  it("keeps retired profiles visible and requires choosing a replacement model", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([
      savedProfile({ model: "legacy-model", configuration_error: "OpenAI 모델과 인증을 다시 선택해 주세요" }),
    ]);
    await showPage();
    expect(screen.getByText("legacy-model")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /저장된 연결/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByRole("alert")).toHaveTextContent("OpenAI 모델과 인증을 다시 선택해 주세요");
    expect(dialog.getByRole("button", { name: "저장" })).toBeDisabled();
    expect(api.saveLLMProfile).not.toHaveBeenCalled();
  });

  it("disables activation of an API draft without a key", async () => {
    vi.mocked(api.llmProfiles).mockResolvedValue([savedProfile({ connected: false, api_key_hint: undefined })]);
    await showPage();
    expect(screen.getByRole("button", { name: "활성으로 설정" })).toBeDisabled();
    expect(api.activateLLMProfile).not.toHaveBeenCalled();
  });
});
