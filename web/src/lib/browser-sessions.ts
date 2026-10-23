import { ApiError, http } from "@/lib/api";
import { auth } from "@/lib/auth";

export type BrowserSession = {
  id: string;
  task_id?: number;
  intent_id?: number;
  conversation_id?: string;
  agent: string;
  state: "active" | "waiting" | "idle";
  reason?: string;
  url: string;
  title: string;
  viewport: { width: number; height: number };
  tabs: { index: number; title: string; url: string }[];
};

export type BrowserAction = {
  action: "click" | "drag" | "type" | "key" | "scroll" | "navigate" | "select_tab";
  x?: number;
  y?: number;
  to_x?: number;
  to_y?: number;
  text?: string;
  key?: string;
  url?: string;
  index?: number;
};

const path = (id: string) => `/browser/sessions/${encodeURIComponent(id)}`;

export const browserSessions = {
  list: async (signal?: AbortSignal) =>
    (await http<{ sessions: BrowserSession[] }>("/browser/sessions", { signal })).sessions,
  takeover: (id: string, signal?: AbortSignal) => http(`${path(id)}/takeover`, { method: "POST", signal }),
  resume: (id: string, signal?: AbortSignal) => http(`${path(id)}/resume`, { method: "POST", signal }),
  remove: (id: string, signal?: AbortSignal) => http(path(id), { method: "DELETE", signal }),
  action: (id: string, action: BrowserAction, signal?: AbortSignal) =>
    http(`${path(id)}/action`, { method: "POST", body: JSON.stringify(action), signal }),
  screenshot: async (id: string, signal?: AbortSignal): Promise<Blob> => {
    const response = await fetch(`/api${path(id)}/screenshot`, {
      signal,
      cache: "no-store",
      headers: { Authorization: `Bearer ${auth.getToken() ?? ""}` },
    });
    if (!response.ok) {
      throw new ApiError("browserScreenshotFailed", response.status);
    }
    if (!response.headers.get("Content-Type")?.startsWith("image/")) {
      throw new Error("browserInvalidScreenshot");
    }
    return response.blob();
  },
};

export function browserPoint(clientX: number, clientY: number, rect: DOMRect, width: number, height: number) {
  return {
    x: Math.max(0, Math.min(width - 1, Math.round(((clientX - rect.left) / rect.width) * width))),
    y: Math.max(0, Math.min(height - 1, Math.round(((clientY - rect.top) / rect.height) * height))),
  };
}
