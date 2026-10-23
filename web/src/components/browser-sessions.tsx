"use client";

import * as React from "react";

import { MonitorIcon, RefreshCwIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { type BrowserAction, type BrowserSession, browserPoint, browserSessions } from "@/lib/browser-sessions";

const stateLabel = (tf: ReturnType<typeof useTranslations>) => ({
  active: tf("agentActive"),
  waiting: tf("waitingForYourInput"),
  idle: tf("idle"),
});

function sessionLabel(session: BrowserSession, tf: ReturnType<typeof useTranslations>) {
  const scope = session.task_id
    ? tf("browserTask", { id: session.task_id })
    : tf("browserChat", { id: session.conversation_id ?? "" });
  return `${scope}${session.intent_id ? tf("browserIntent", { id: session.intent_id }) : ""} · ${session.agent}`;
}

// Passwords and OTPs remain in this component until sent to the controlled page.
// They are never put into a chat message, URL, persistent preference or toast.
export function BrowserSessions() {
  const tf = useTranslations("forkUI");
  const [open, setOpen] = React.useState(false);
  const [sessions, setSessions] = React.useState<BrowserSession[]>([]);
  const [selected, setSelected] = React.useState("");
  const [error, setError] = React.useState("");
  const [revision, refresh] = React.useReducer((n: number) => n + 1, 0);
  const session = sessions.find((item) => item.id === selected);
  const waiting = sessions.filter((item) => item.state === "waiting").length;

  // biome-ignore lint/correctness/useExhaustiveDependencies: explicit refresh cancels the previous polling generation.
  React.useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const rows = await browserSessions.list(controller.signal);
        if (controller.signal.aborted) return;
        setSessions(rows);
        setSelected((current) =>
          rows.some((item) => item.id === current)
            ? current
            : (rows.find((item) => item.state === "waiting")?.id ?? rows[0]?.id ?? ""),
        );
        setError("");
      } catch (err) {
        if (!controller.signal.aborted) setError((err as Error).message);
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(poll, open ? 2000 : 5000);
      }
    };
    void poll();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [open, revision]);

  return (
    <>
      <Button
        variant={waiting ? "default" : "outline"}
        className="fixed right-4 bottom-4 z-40 shadow-md"
        onClick={() => setOpen(true)}
        aria-label={waiting ? tf("browserWaitingCount", { count: waiting }) : tf("browserSessions")}
      >
        <MonitorIcon className="size-4" />
        {tf("browserSessions")}
        {waiting > 0 && <Badge variant="secondary">{waiting}</Badge>}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-6xl">
          <DialogHeader>
            <DialogTitle>{tf("browserSessions")}</DialogTitle>
            <DialogDescription>{tf("completeSigninOTPOrVerificationInThe")}</DialogDescription>
          </DialogHeader>
          {error && (
            <p role="alert" className="text-destructive">
              {error}
            </p>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <select
              aria-label={tf("selectBrowserSession")}
              className="min-w-0 flex-1 rounded-md border bg-background p-2"
              value={selected}
              onChange={(event) => setSelected(event.target.value)}
            >
              {sessions.length === 0 && <option value="">{tf("noBrowserSessionsAreOpen")}</option>}
              {sessions.map((item) => (
                <option key={item.id} value={item.id}>
                  {sessionLabel(item, tf)} · {stateLabel(tf)[item.state]}
                </option>
              ))}
            </select>
            <Button variant="outline" size="icon" aria-label={tf("refreshSessions")} onClick={refresh}>
              <RefreshCwIcon className="size-4" />
            </Button>
          </div>
          {open && session && <BrowserControl key={session.id} session={session} onChanged={refresh} />}
          {!session && (
            <p className="py-8 text-center text-muted-foreground">{tf("browserSessionsAppearHereWhenAnAgent")}</p>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

export function BrowserControl({ session, onChanged }: { session: BrowserSession; onChanged: () => void }) {
  const tf = useTranslations("forkUI");
  const [image, setImage] = React.useState("");
  const [text, setText] = React.useState("");
  const [error, setError] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [revision, refresh] = React.useReducer((n: number) => n + 1, 0);
  const life = React.useRef<AbortController | null>(null);
  const pointer = React.useRef<{ x: number; y: number } | null>(null);
  const busy = React.useRef(false);
  const controlled = session.state === "waiting";

  React.useEffect(() => {
    const controller = new AbortController();
    life.current = controller;
    return () => {
      controller.abort();
    };
  }, []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: a completed action immediately refreshes the screenshot.
  React.useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    let currentURL = "";
    setImage("");
    const capture = async () => {
      try {
        const blob = await browserSessions.screenshot(session.id, controller.signal);
        if (controller.signal.aborted) return;
        const next = URL.createObjectURL(blob);
        if (currentURL) URL.revokeObjectURL(currentURL);
        currentURL = next;
        setImage(next);
      } catch (err) {
        if (!controller.signal.aborted) {
          const message = (err as Error).message;
          setError(tf.has(message) ? tf(message) : message);
        }
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(capture, controlled ? 1500 : 4000);
      }
    };
    void capture();
    return () => {
      controller.abort();
      clearTimeout(timer);
      if (currentURL) URL.revokeObjectURL(currentURL);
    };
  }, [session.id, controlled, revision, tf]);

  const run = async (operation: (signal?: AbortSignal) => Promise<unknown>) => {
    if (busy.current) return;
    busy.current = true;
    setPending(true);
    setError("");
    const signal = life.current?.signal;
    try {
      await operation(signal);
      if (!signal?.aborted) {
        refresh();
        onChanged();
      }
    } catch (err) {
      if (!signal?.aborted) setError((err as Error).message);
    } finally {
      busy.current = false;
      if (!signal?.aborted) setPending(false);
    }
  };
  const action = (value: BrowserAction) => {
    if (controlled) void run((signal) => browserSessions.action(session.id, value, signal));
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={controlled ? "default" : "secondary"}>{stateLabel(tf)[session.state]}</Badge>
        <p className="min-w-0 flex-1 truncate text-sm" title={session.url}>
          {session.title || session.url || tf("blankPage")}
        </p>
        {controlled ? (
          <Button
            disabled={pending}
            onClick={() => {
              setText("");
              void run((signal) => browserSessions.resume(session.id, signal));
            }}
          >
            {tf("doneReturnControlToAgent")}
          </Button>
        ) : (
          <Button disabled={pending} onClick={() => void run((signal) => browserSessions.takeover(session.id, signal))}>
            {tf("takeControl")}
          </Button>
        )}
        {session.state === "idle" && (
          <Button
            variant="outline"
            disabled={pending}
            onClick={() => {
              if (window.confirm(tf("browserDeleteConfirm")))
                void run((signal) => browserSessions.remove(session.id, signal));
            }}
          >
            {tf("browserDelete")}
          </Button>
        )}
      </div>
      {session.reason && <p className="rounded-md bg-muted p-2 text-sm">{session.reason}</p>}
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
      {session.tabs.length > 1 && (
        <select
          aria-label={tf("selectBrowserTab")}
          className="w-full rounded-md border bg-background p-2"
          defaultValue=""
          disabled={!controlled || pending}
          onChange={(event) => action({ action: "select_tab", index: Number(event.target.value) })}
        >
          <option value="" disabled>
            {tf("chooseATabToControl")}
          </option>
          {session.tabs.map((tab) => (
            <option key={tab.index} value={tab.index}>
              {tab.title || tab.url || tf("browserTab", { index: tab.index + 1 })}
            </option>
          ))}
        </select>
      )}
      <div className="overflow-hidden rounded-md border bg-muted">
        {image ? (
          <button
            type="button"
            aria-label={tf("controlTheBrowserScreen")}
            className="block w-full touch-none"
            disabled={!controlled || pending}
            onPointerDown={(event) => {
              if (event.button !== 0) return;
              event.currentTarget.setPointerCapture(event.pointerId);
              pointer.current = browserPoint(
                event.clientX,
                event.clientY,
                event.currentTarget.getBoundingClientRect(),
                session.viewport.width,
                session.viewport.height,
              );
            }}
            onPointerCancel={() => {
              pointer.current = null;
            }}
            onPointerUp={(event) => {
              const start = pointer.current;
              pointer.current = null;
              if (!start) return;
              const end = browserPoint(
                event.clientX,
                event.clientY,
                event.currentTarget.getBoundingClientRect(),
                session.viewport.width,
                session.viewport.height,
              );
              const drag = Math.abs(start.x - end.x) + Math.abs(start.y - end.y) > 5;
              action(drag ? { action: "drag", ...start, to_x: end.x, to_y: end.y } : { action: "click", ...end });
            }}
          >
            {/* Authenticated screenshot URLs are short-lived object URLs, never query-string tokens. */}
            {/* biome-ignore lint/performance/noImgElement: live local browser capture cannot use Next image optimization. */}
            <img src={image} alt={tf("currentAgentBrowserScreen")} draggable={false} className="block h-auto w-full" />
          </button>
        ) : (
          <p className="py-12 text-center text-muted-foreground">{tf("loadingScreen")}</p>
        )}
      </div>
      {controlled && (
        <>
          <form
            className="flex gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              if (!text || pending) return;
              const value = text;
              setText("");
              action({ action: "type", text: value });
            }}
          >
            <Input
              aria-label={tf("textToSendToTheFocusedField")}
              type="password"
              autoComplete="off"
              value={text}
              placeholder={tf("clickAFieldOnTheScreenThen")}
              onChange={(event) => setText(event.target.value)}
              disabled={pending}
            />
            <Button type="submit" disabled={pending || !text}>
              {tf("sendText")}
            </Button>
          </form>
          <div className="flex flex-wrap gap-2">
            {["Tab", "Enter", "Backspace", "Escape", "ControlOrMeta+A"].map((key) => (
              <Button
                key={key}
                variant="outline"
                size="sm"
                disabled={pending}
                onClick={() => action({ action: "key", key })}
              >
                {key === "ControlOrMeta+A" ? tf("selectAll") : key}
              </Button>
            ))}
            <Button
              variant="outline"
              size="sm"
              disabled={pending}
              onClick={() => action({ action: "scroll", x: 0, y: -500 })}
            >
              {tf("scrollUp")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={pending}
              onClick={() => action({ action: "scroll", x: 0, y: 500 })}
            >
              {tf("scrollDown")}
            </Button>
          </div>
          <p className="text-muted-foreground text-xs">{tf("yourInputIsNotSentToChat")}</p>
        </>
      )}
    </div>
  );
}
