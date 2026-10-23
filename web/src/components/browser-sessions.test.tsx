import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type BrowserSession, browserPoint, browserSessions } from "@/lib/browser-sessions";

import enMessages from "../../messages/en.json";
import { BrowserControl } from "./browser-sessions";

vi.mock("@/lib/browser-sessions", async (original) => ({
  ...(await original<typeof import("@/lib/browser-sessions")>()),
  browserSessions: { screenshot: vi.fn(), action: vi.fn(), resume: vi.fn(), takeover: vi.fn(), remove: vi.fn() },
}));

const session: BrowserSession = {
  id: "session-a",
  agent: "worker",
  state: "waiting",
  title: "Local login fixture",
  url: "http://fixture.test",
  viewport: { width: 1280, height: 800 },
  tabs: [],
};

function pending<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

const view = (value: BrowserSession, onChanged = vi.fn()) => (
  <NextIntlClientProvider locale="en" messages={enMessages}>
    <BrowserControl key={value.id} session={value} onChanged={onChanged} />
  </NextIntlClientProvider>
);

describe("browser handoff", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.stubGlobal(
      "URL",
      Object.assign(URL, { createObjectURL: vi.fn(() => "blob:current"), revokeObjectURL: vi.fn() }),
    );
    vi.mocked(browserSessions.screenshot).mockResolvedValue(new Blob(["fixture"], { type: "image/png" }));
    vi.mocked(browserSessions.action).mockResolvedValue({});
    vi.mocked(browserSessions.remove).mockResolvedValue({});
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("keeps the new session screenshot when an older response arrives late", async () => {
    const first = pending<Blob>();
    vi.mocked(browserSessions.screenshot).mockReturnValueOnce(first.promise);
    const rendered = render(view(session));
    const oldSignal = vi.mocked(browserSessions.screenshot).mock.calls[0][1];
    await act(async () => rendered.rerender(view({ ...session, id: "session-b" })));
    expect(oldSignal?.aborted).toBe(true);
    expect(screen.getByRole("img")).toHaveAttribute("src", "blob:current");
    await act(async () => first.resolve(new Blob(["old"])));
    expect(URL.createObjectURL).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Done · Return control to agent" })).toBeInTheDocument();
  });

  it("sends a password only to the selected browser and ignores completion after unmount", async () => {
    const request = pending<unknown>();
    vi.mocked(browserSessions.action).mockReturnValueOnce(request.promise);
    const changed = vi.fn();
    const rendered = render(view(session, changed));
    const input = screen.getByLabelText("Text to send to the focused field");
    fireEvent.change(input, { target: { value: "fixture-password" } });
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Send text" })));
    expect(input).toHaveValue("");
    expect(browserSessions.action).toHaveBeenCalledWith(
      "session-a",
      { action: "type", text: "fixture-password" },
      expect.any(AbortSignal),
    );
    const signal = vi.mocked(browserSessions.action).mock.calls[0][2];
    rendered.unmount();
    expect(signal?.aborted).toBe(true);
    await act(async () => request.resolve({}));
    expect(changed).not.toHaveBeenCalled();
  });

  it("requires confirmation before deleting an idle session with its cookies", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    render(view({ ...session, state: "idle" }));
    const button = screen.getByRole("button", { name: "Delete session" });
    await act(async () => fireEvent.click(button));
    expect(browserSessions.remove).not.toHaveBeenCalled();
    await act(async () => fireEvent.click(button));
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining("cookies"));
    expect(browserSessions.remove).toHaveBeenCalledWith("session-a", expect.any(AbortSignal));
  });

  it("maps scaled screen coordinates and clamps outside clicks", () => {
    const rect = { left: 10, top: 20, width: 640, height: 400 } as DOMRect;
    expect(browserPoint(330, 220, rect, 1280, 800)).toEqual({ x: 640, y: 400 });
    expect(browserPoint(-30, 9999, rect, 1280, 800)).toEqual({ x: 0, y: 799 });
  });
});
