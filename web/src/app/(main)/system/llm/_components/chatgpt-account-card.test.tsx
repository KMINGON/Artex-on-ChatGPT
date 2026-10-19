import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "@/lib/api";
import type { ChatGPTAccount } from "@/lib/types";

import { ChatGPTAccountCard } from "./chatgpt-account-card";

vi.mock("@/lib/api", () => ({
  api: { chatgptStatus: vi.fn(), chatgptLogin: vi.fn(), chatgptLogout: vi.fn() },
}));
vi.mock("sonner", () => ({ toast: { warning: vi.fn() } }));

const account: ChatGPTAccount = {
  id: "account-reference",
  email: "person@example.test",
  connected: true,
  plan_enabled: true,
};

describe("ChatGPT account connection", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetAllMocks();
    vi.spyOn(window, "open").mockReturnValue(null);
    vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [], pending: false });
    vi.mocked(api.chatgptLogin).mockResolvedValue({
      url: "https://auth.openai.com/authorize?state=login-state",
      expires_at: new Date(Date.now() + 60_000).toISOString(),
    });
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  async function showCard() {
    const onAccounts = vi.fn();
    const onChanged = vi.fn().mockResolvedValue(undefined);
    let view: ReturnType<typeof render>;
    await act(async () => {
      view = render(<ChatGPTAccountCard onAccounts={onAccounts} onChanged={onChanged} />);
    });
    return { unmount: () => view.unmount(), onAccounts, onChanged };
  }

  it("offers a login link when popups are blocked, then refreshes profiles and stops polling after completion", async () => {
    const { onAccounts, onChanged } = await showCard();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "ChatGPT로 계속" })));

    expect(api.chatgptLogin).toHaveBeenCalledWith(undefined);
    expect(screen.getByRole("link", { name: "로그인 화면 열기" })).toHaveAttribute(
      "href",
      "https://auth.openai.com/authorize?state=login-state",
    );
    vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [account], pending: false });
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    expect(onAccounts).toHaveBeenLastCalledWith([account]);
    expect(onChanged).toHaveBeenCalledOnce();
    expect(screen.getByText(account.email)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "로그인 화면 열기" })).not.toBeInTheDocument();

    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    expect(api.chatgptStatus).toHaveBeenCalledTimes(2);
  });

  it("does not poll after leaving the page", async () => {
    const { unmount } = await showCard();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "ChatGPT로 계속" })));
    unmount();
    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    expect(api.chatgptStatus).toHaveBeenCalledOnce();
  });

  it("bounds waiting even when a status request never returns", async () => {
    vi.mocked(api.chatgptLogin).mockResolvedValue({
      url: "https://auth.openai.com/authorize",
      expires_at: new Date(Date.now() + 5000).toISOString(),
    });
    await showCard();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "ChatGPT로 계속" })));
    vi.mocked(api.chatgptStatus).mockImplementation(() => new Promise(() => undefined));
    await act(async () => vi.advanceTimersByTimeAsync(6000));
    expect(screen.getByRole("alert")).toHaveTextContent("로그인 확인 시간이 지났습니다");
    expect(screen.getByRole("button", { name: "ChatGPT로 계속" })).toBeEnabled();
    expect(screen.queryByRole("link", { name: "로그인 화면 열기" })).not.toBeInTheDocument();
  });

  it("rejects non-HTTPS login redirects and does not begin polling", async () => {
    vi.mocked(api.chatgptLogin).mockResolvedValue({ url: "javascript:alert(1)", expires_at: "" });
    await showCard();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "ChatGPT로 계속" })));
    expect(screen.getByRole("alert")).toHaveTextContent("올바른 로그인 주소");
    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    expect(api.chatgptStatus).toHaveBeenCalledOnce();
  });

  it("disconnects using only the account reference and refreshes profile readiness", async () => {
    vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [account], pending: false });
    vi.mocked(api.chatgptLogout).mockResolvedValue({ ok: true });
    const { onChanged } = await showCard();
    vi.mocked(api.chatgptStatus).mockResolvedValue({ accounts: [], pending: false });
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "연결 해제" })));
    expect(api.chatgptLogout).toHaveBeenCalledWith(account.id);
    expect(onChanged).toHaveBeenCalledOnce();
    expect(screen.queryByText(account.email)).not.toBeInTheDocument();
  });
});
