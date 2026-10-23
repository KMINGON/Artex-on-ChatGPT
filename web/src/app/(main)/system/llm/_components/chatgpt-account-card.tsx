"use client";

import * as React from "react";

import { ExternalLinkIcon, Loader2Icon, RefreshCwIcon } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import { chatGPTAccountLabel } from "@/lib/chatgpt";
import type { ChatGPTAccount, ChatGPTStatus, LLMProfile } from "@/lib/types";

const MAX_LOGIN_WAIT_MS = 10 * 60 * 1000;

export function ChatGPTAccountCard({
  onAccounts,
  onChanged,
  profiles = [],
}: {
  onAccounts: (accounts: ChatGPTAccount[]) => void;
  onChanged: () => Promise<void>;
  profiles?: LLMProfile[];
}) {
  const tf = useTranslations("forkUI");
  const [status, setStatus] = React.useState<ChatGPTStatus>({ accounts: [], pending: false });
  const [loading, setLoading] = React.useState(true);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");
  const [loginUrl, setLoginUrl] = React.useState("");
  const [deadline, setDeadline] = React.useState(0);
  const [probeProfiles, setProbeProfiles] = React.useState<Record<string, string>>({});

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
        setError(tf("signinVerificationTimedOutRefreshTheStatus"));
      },
      Math.max(0, deadline - Date.now()),
    );
    const poll = async () => {
      if (!active) return;
      if (Date.now() >= deadline) {
        setDeadline(0);
        setLoginUrl("");
        setError(tf("signinVerificationTimedOutRefreshTheStatus"));
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
  }, [deadline, acceptStatus, onChanged, tf]);

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
      if (url.protocol !== "https:") throw new Error(tf("theServerReturnedAnInvalidSigninURL"));
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

  async function recheck(profileId: string) {
    if (busy || !profileId) return;
    setBusy(true);
    setError("");
    try {
      const result = await api.chatgptRecheck(profileId);
      await refresh();
      if (!result.ok) setError(result.error ?? tf("couldNotRecheckTheUsageLimit"));
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
        <CardTitle>{tf("chatGPTAccounts")}</CardTitle>
        <CardDescription>{tf("thisModelConnectionIsSeparateFromThe")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <p className="text-muted-foreground text-sm">{tf("chatGPTPlanUsageLimitsApplyRequestsDo")}</p>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button onClick={() => void login()} disabled={loading || busy || waiting}>
            {(busy || waiting) && <Loader2Icon className="animate-spin" />}
            {tf("continueWithChatGPT")}
          </Button>
          <Button variant="outline" onClick={() => void refresh()} disabled={loading || busy}>
            <RefreshCwIcon className={loading ? "animate-spin" : ""} /> {tf("refreshConnectionStatus")}
          </Button>
        </div>
        {waiting && (
          <p role="status" className="text-muted-foreground text-sm">
            {tf("completeSigninInYourBrowserTheAccount")}
          </p>
        )}
        {loginUrl && (
          <a
            href={loginUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-sm underline"
          >
            {tf("openSigninPage")}
            <ExternalLinkIcon className="size-3.5" />
          </a>
        )}
        {!loading && status.accounts.length === 0 && (
          <p className="text-muted-foreground text-sm">{tf("noAccountsConnected")}</p>
        )}
        {status.accounts.map((account) => {
          const candidates = profiles.filter(
            (profile) =>
              profile.auth_mode === "chatgpt_plan" &&
              profile.chatgpt_account_id === account.id &&
              !profile.configuration_error,
          );
          const profileId = probeProfiles[account.id] ?? candidates[0]?.id ?? "";
          const label = chatGPTAccountLabel(account);
          return (
            <fieldset
              key={account.id}
              aria-label={label}
              className="flex flex-wrap items-center gap-2 rounded-lg border p-3"
            >
              <span className="min-w-0 flex-1 break-all text-sm">{label}</span>
              <Badge variant="outline">
                {!account.connected && tf("reconnectRequired")}
                {account.connected && account.plan_blocked && tf("usageLimitRecheckRequired")}
                {account.connected &&
                  !account.plan_blocked &&
                  (account.plan_enabled ? tf("connected") : tf("planUnavailable"))}
              </Badge>
              <Button size="sm" variant="outline" onClick={() => void login(account.id)} disabled={busy || waiting}>
                {tf("reconnect")}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => void logout(account.id)} disabled={busy || waiting}>
                {tf("disconnect")}
              </Button>
              {account.plan_blocked && (
                <div className="grid w-full gap-2 border-t pt-3">
                  <p className="text-sm">
                    {tf("newModelRequestsForThisConnectionAre")}{" "}
                    <a className="underline" href="https://chatgpt.com/settings/usage" target="_blank" rel="noreferrer">
                      {tf("viewChatGPTUsage")}
                    </a>
                  </p>
                  {account.plan_blocked_at && (
                    <p className="text-muted-foreground text-xs">
                      {tf("checkedAt")}
                      {account.plan_blocked_at}
                    </p>
                  )}
                  <Label htmlFor={`recheck-${account.id}`}>{tf("savedProfileToUseForRecheck")}</Label>
                  <Select
                    value={profileId || "_none"}
                    onValueChange={(value) => setProbeProfiles((previous) => ({ ...previous, [account.id]: value }))}
                  >
                    <SelectTrigger id={`recheck-${account.id}`}>
                      <SelectValue placeholder={tf("saveAProfileFirst")} />
                    </SelectTrigger>
                    <SelectContent>
                      {candidates.length === 0 && (
                        <SelectItem value="_none">{tf("noAvailableSavedProfiles")}</SelectItem>
                      )}
                      {candidates.map((profile) => (
                        <SelectItem key={profile.id} value={profile.id}>
                          {profile.name} · {profile.model}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-muted-foreground text-xs">{tf("sendsOneRealRequestToTheSelected")}</p>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => void recheck(profileId)}
                    disabled={
                      busy ||
                      waiting ||
                      account.rechecking ||
                      !account.connected ||
                      !account.plan_enabled ||
                      !candidates.some((profile) => profile.id === profileId)
                    }
                  >
                    {tf("recheckUsageLimit")}
                  </Button>
                </div>
              )}
            </fieldset>
          );
        })}
      </CardContent>
    </Card>
  );
}
