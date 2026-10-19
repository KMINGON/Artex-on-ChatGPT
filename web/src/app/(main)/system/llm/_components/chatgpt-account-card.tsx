"use client";

import * as React from "react";

import { ExternalLinkIcon, Loader2Icon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { ChatGPTAccount, ChatGPTStatus } from "@/lib/types";

const MAX_LOGIN_WAIT_MS = 10 * 60 * 1000;

export function ChatGPTAccountCard({
  onAccounts,
  onChanged,
}: {
  onAccounts: (accounts: ChatGPTAccount[]) => void;
  onChanged: () => Promise<void>;
}) {
  const [status, setStatus] = React.useState<ChatGPTStatus>({ accounts: [], pending: false });
  const [loading, setLoading] = React.useState(true);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");
  const [loginUrl, setLoginUrl] = React.useState("");
  const [deadline, setDeadline] = React.useState(0);

  const acceptStatus = React.useCallback(
    (next: ChatGPTStatus) => {
      setStatus(next);
      onAccounts(next.accounts);
      setError(next.error ?? "");
    },
    [onAccounts],
  );

  React.useEffect(() => {
    let active = true;
    api
      .chatgptStatus()
      .then((next) => {
        if (!active) return;
        acceptStatus(next);
        if (next.pending) setDeadline(Date.now() + MAX_LOGIN_WAIT_MS);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [acceptStatus]);

  React.useEffect(() => {
    if (!deadline) return;
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const expire = setTimeout(
      () => {
        active = false;
        clearTimeout(timer);
        setDeadline(0);
        setLoginUrl("");
        setError("로그인 확인 시간이 지났습니다. 상태를 새로고침하거나 다시 연결해 주세요.");
      },
      Math.max(0, deadline - Date.now()),
    );
    const poll = async () => {
      if (!active) return;
      if (Date.now() >= deadline) {
        setDeadline(0);
        setLoginUrl("");
        setError("로그인 확인 시간이 지났습니다. 상태를 새로고침하거나 다시 연결해 주세요.");
        return;
      }
      try {
        const next = await api.chatgptStatus();
        if (!active) return;
        acceptStatus(next);
        if (!next.pending) {
          setDeadline(0);
          setLoginUrl("");
          await onChanged();
          return;
        }
      } catch (e) {
        if (active) setError((e as Error).message);
      }
      if (active) timer = setTimeout(() => void poll(), 2000);
    };
    timer = setTimeout(() => void poll(), 2000);
    return () => {
      active = false;
      clearTimeout(timer);
      clearTimeout(expire);
    };
  }, [deadline, acceptStatus, onChanged]);

  async function refresh() {
    setLoading(true);
    try {
      const next = await api.chatgptStatus();
      acceptStatus(next);
      setDeadline(next.pending ? Date.now() + MAX_LOGIN_WAIT_MS : 0);
      if (!next.pending) setLoginUrl("");
      await onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }

  async function login(accountId?: string) {
    if (busy) return;
    // Open during the click so the browser's popup blocker does not lose the user gesture.
    const popup = window.open("about:blank", "_blank");
    if (popup) popup.opener = null;
    setBusy(true);
    setError("");
    try {
      const result = await api.chatgptLogin(accountId);
      const url = new URL(result.url);
      if (url.protocol !== "https:") throw new Error("서버가 올바른 로그인 주소를 반환하지 않았습니다.");
      setLoginUrl(url.href);
      setStatus((previous) => ({ ...previous, pending: true }));
      const expiresAt = Date.parse(result.expires_at);
      setDeadline(Math.min(Number.isFinite(expiresAt) ? expiresAt : Infinity, Date.now() + MAX_LOGIN_WAIT_MS));
      if (popup) popup.location.href = url.href;
    } catch (e) {
      popup?.close();
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function logout(accountId: string) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await api.chatgptLogout(accountId);
      if (result.warning) toast.warning(result.warning);
      acceptStatus(await api.chatgptStatus());
      await onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const waiting = deadline > 0;
  return (
    <Card>
      <CardHeader>
        <CardTitle>ChatGPT 계정 연결</CardTitle>
        <CardDescription>
          ARTEX 관리자 로그인과 별개인 모델 연결입니다. OpenAI 로그인 화면에서 계정을 연결한 뒤 모델 설정에서
          선택하세요.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <p className="text-muted-foreground text-sm">
          ChatGPT 요금제의 사용 한도가 적용되며, API 키 설정으로 자동 전환하지 않습니다. 응답 품질이나 속도를 보장하지
          않습니다.
        </p>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button onClick={() => void login()} disabled={loading || busy || waiting}>
            {(busy || waiting) && <Loader2Icon className="animate-spin" />}
            ChatGPT로 계속
          </Button>
          <Button variant="outline" onClick={() => void refresh()} disabled={loading || busy}>
            <RefreshCwIcon className={loading ? "animate-spin" : ""} /> 연결 상태 새로고침
          </Button>
        </div>
        {waiting && (
          <p role="status" className="text-muted-foreground text-sm">
            브라우저에서 로그인을 완료해 주세요. 완료되면 계정 상태를 자동으로 갱신합니다.
          </p>
        )}
        {loginUrl && (
          <a
            href={loginUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-sm underline"
          >
            로그인 화면 열기 <ExternalLinkIcon className="size-3.5" />
          </a>
        )}
        {!loading && status.accounts.length === 0 && (
          <p className="text-muted-foreground text-sm">연결된 계정이 없습니다.</p>
        )}
        {status.accounts.map((account) => (
          <div key={account.id} className="flex flex-wrap items-center gap-2 rounded-lg border p-3">
            <span className="min-w-0 flex-1 break-all text-sm">{account.email || account.id}</span>
            <Badge variant="outline">
              {!account.connected && "다시 연결 필요"}
              {account.connected && (account.plan_enabled ? "연결됨" : "요금제 사용 불가")}
            </Badge>
            <Button size="sm" variant="outline" onClick={() => void login(account.id)} disabled={busy || waiting}>
              다시 연결
            </Button>
            <Button size="sm" variant="ghost" onClick={() => void logout(account.id)} disabled={busy || waiting}>
              연결 해제
            </Button>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}
