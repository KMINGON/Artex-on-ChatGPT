"use client";

import * as React from "react";

import {
  CheckIcon,
  Loader2Icon,
  PlugZapIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SaveIcon,
  StarIcon,
  Trash2Icon,
  ZapIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { getDateLocale } from "@/i18n/datetime";
import { api } from "@/lib/api";
import { chatGPTAccountLabel } from "@/lib/chatgpt";
import type {
  ChatGPTAccount,
  ChatGPTModel,
  LLMAuthMode,
  LLMPoolMember,
  LLMPoolStatus,
  LLMProfile,
  LLMRetryOverride,
} from "@/lib/types";
import { cn } from "@/lib/utils";

import { ChatGPTAccountCard } from "./_components/chatgpt-account-card";
import { ProfileRetryFields, RetryPolicyPanel, ZERO_OVERRIDE } from "./_components/retry";

// Empty effort means the model default; the select uses a nonempty sentinel.
const NONE = "_default";
const PROGRAM_LABELS: Record<string, string> = {
  standard: "Standard",
  daybreak_blue: "Daybreak Blue",
  daybreak_red: "Daybreak Red",
};
const fromStore = (v?: string) => (v ? v : NONE);
const toStore = (v: string) => (v === NONE ? "" : v);

function programLabel(program: string | undefined, tf: ReturnType<typeof useTranslations>) {
  return program ? PROGRAM_LABELS[program] || program : tf("programUnverified");
}

function catalogProgramLabel(model: ChatGPTModel, tf: ReturnType<typeof useTranslations>) {
  const c = model.capabilities;
  if (c?.source !== "account_catalog" || !c.access_programs_known) return tf("unverified");
  const programs = c.access_programs_cyber ?? [];
  if (programs.includes("daybreak_blue") || programs.includes("daybreak_red")) return "Daybreak";
  return programs.includes("standard") ? "Standard" : tf("unverified");
}

function cooldownText(secs: number) {
  if (secs <= 0) return "";
  if (secs < 60) return `${secs}s`;
  return `${Math.ceil(secs / 60)}min`;
}

// 一个配置在卡片上显示的「是否正常」。没填 Key 的配置根本发不出请求，比熔断更该先说；
// 其余状态来自轮询的熔断记录（轮询关着时不会产生新记录，此时「正常」= 没有已知故障）。
// 文案走 i18n（llmPage.health.*），所以把 t 传进来由调用方注入。
type Health = { label: string; cls: string; hint?: string };

// ─────────────────────────────────────────────────────────────────────────────
// 轮询配置抽屉
// ─────────────────────────────────────────────────────────────────────────────

function PoolSheet({
  open,
  onOpenChange,
  pool,
  onReload,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  pool: LLMPoolStatus | null;
  onReload: () => Promise<void>;
}) {
  const t = useTranslations("llmPage");
  const [busy, setBusy] = React.useState(false);

  // 冷却倒计时是后端算出的剩余秒数——抽屉开着且有配置不正常时才定时拉，让它走起来。
  React.useEffect(() => {
    if (!open || !pool?.enabled || !pool.chain.some((m) => m.state !== "ok")) return;
    const t = setInterval(() => void onReload(), 10_000);
    return () => clearInterval(t);
  }, [open, pool, onReload]);

  async function toggle(patch: { llm_pool_enabled?: boolean; llm_pool_bind_fallback?: boolean }) {
    if (busy) return;
    setBusy(true);
    try {
      await api.setSettings(patch);
      await onReload();
      if (patch.llm_pool_enabled !== undefined) {
        toast.success(patch.llm_pool_enabled ? t("pool.toast.enabled") : t("pool.toast.disabled"));
      } else {
        toast.success(t("pool.toast.fallbackUpdated"));
      }
    } catch (e) {
      toast.error(t("pool.toast.setFailed", { msg: (e as Error).message }));
    } finally {
      setBusy(false);
    }
  }

  async function recover(id?: string) {
    try {
      await api.resetLLMPool(id);
      await onReload();
      toast.success(id ? t("pool.toast.recoveredOne") : t("pool.toast.recoveredAll"));
    } catch (e) {
      toast.error(t("pool.toast.recoverFailed", { msg: (e as Error).message }));
    }
  }

  const enabled = pool?.enabled ?? false;
  const chain = pool?.chain ?? [];
  // 参与轮询的成员（排除被标记「不参与轮询」的），顺序即后端实际的尝试顺序。
  const inChain = chain.filter((m) => m.active || !m.excluded);
  const tripped = chain.filter((m) => m.state === "tripped");

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex flex-col gap-0 p-0 data-[side=right]:sm:max-w-lg">
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            <ZapIcon className="size-4" /> {t("pool.title")}
          </SheetTitle>
          <SheetDescription>{t.rich("pool.desc", { b: (c) => <b>{c}</b> })}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-6">
          <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
            <div className="grid gap-0.5">
              <Label className="text-sm">{t("pool.enable.label")}</Label>
              <p className="text-muted-foreground text-xs">{t("pool.enable.hint")}</p>
            </div>
            <Switch
              checked={enabled}
              disabled={busy}
              onCheckedChange={(v) => void toggle({ llm_pool_enabled: v })}
              aria-label={t("pool.enable.aria")}
            />
          </div>

          {enabled && (
            <>
              <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="grid gap-0.5">
                  <Label className="text-sm">{t("pool.bindFallback.label")}</Label>
                  <p className="text-muted-foreground text-xs">{t("pool.bindFallback.hint")}</p>
                </div>
                <Switch
                  checked={pool?.bind_fallback ?? false}
                  disabled={busy}
                  onCheckedChange={(v) => void toggle({ llm_pool_bind_fallback: v })}
                  aria-label={t("pool.bindFallback.aria")}
                />
              </div>

              <Separator />

              <div className="grid gap-2">
                <div className="flex items-center justify-between">
                  <Label className="text-sm">{t("pool.order.label")}</Label>
                  {tripped.length > 0 && (
                    <Button size="sm" variant="ghost" onClick={() => void recover()}>
                      <RotateCcwIcon /> {t("pool.recoverAll")}
                    </Button>
                  )}
                </div>
                {inChain.length < 2 && (
                  <p className="text-muted-foreground text-xs">{t("pool.order.tooFew", { n: inChain.length })}</p>
                )}
                {chain.map((m) => {
                  const excluded = m.excluded && !m.active;
                  const order = excluded ? null : inChain.findIndex((x) => x.profile_id === m.profile_id) + 1;
                  return (
                    <div
                      key={m.profile_id}
                      className={cn(
                        "grid gap-1 rounded-lg border p-2.5 text-sm",
                        excluded && "opacity-55",
                        m.state === "tripped" && "border-destructive/40",
                      )}
                    >
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                        <span className="w-5 shrink-0 text-center font-mono text-muted-foreground text-xs">
                          {order ?? "—"}
                        </span>
                        <span className="font-medium">{m.name}</span>
                        {m.active && (
                          <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                            {t("pool.badge.active")}
                          </Badge>
                        )}
                        {excluded && <Badge variant="outline">{t("pool.badge.excluded")}</Badge>}
                        <div className="ml-auto flex items-center gap-2">
                          {m.state === "tripped" && m.cooldown_secs > 0 && (
                            <span className="text-muted-foreground text-xs">
                              {t("pool.cooldown", { cooldown: cooldownText(m.cooldown_secs) })}
                            </span>
                          )}
                          {m.state === "degraded" && (
                            <span className="text-muted-foreground text-xs">
                              {t("pool.consecutiveFails", { fails: m.fails })}
                            </span>
                          )}
                          {m.state !== "ok" && (
                            <Button
                              size="icon"
                              variant="ghost"
                              className="size-7"
                              aria-label={t("pool.recoverNow.aria")}
                              title={t("pool.recoverNow.title")}
                              onClick={() => void recover(m.profile_id)}
                            >
                              <RotateCcwIcon className="size-3.5" />
                            </Button>
                          )}
                        </div>
                      </div>
                      <div className="flex flex-wrap items-center gap-x-3 pl-7 text-muted-foreground text-xs">
                        <code className="truncate font-mono">{m.model}</code>
                        {!m.active && <span>{t("pool.priority", { priority: m.priority })}</span>}
                      </div>
                      {m.last_error && (
                        <p className="truncate pl-7 font-mono text-muted-foreground text-xs" title={m.last_error}>
                          {m.last_error}
                        </p>
                      )}
                    </div>
                  );
                })}
                {chain.length === 0 && (
                  <div className="rounded-lg border border-dashed p-4 text-center text-muted-foreground text-sm">
                    {t("pool.empty")}
                  </div>
                )}
              </div>

              <div className="rounded-lg border border-dashed p-3 text-muted-foreground text-xs leading-relaxed">
                {t("pool.note")}
              </div>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// 模型配置抽屉（新建 / 编辑共用同一套表单）
// ─────────────────────────────────────────────────────────────────────────────

function ProfileSheet({
  profile,
  accounts,
  open,
  onOpenChange,
  onSaved,
}: {
  profile: LLMProfile | null; // null = 新建
  accounts: ChatGPTAccount[];
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onSaved: (id: string) => void;
}) {
  const t = useTranslations("llmPage");
  const tf = useTranslations("forkUI");
  const isNew = !profile;
  const [name, setName] = React.useState("");
  const [authMode, setAuthMode] = React.useState<LLMAuthMode>("api_key");
  const [accountId, setAccountId] = React.useState("");
  const [model, setModel] = React.useState("");
  const [proxy, setProxy] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [keyHint, setKeyHint] = React.useState("");
  const [rps, setRps] = React.useState("0");
  const [rpm, setRpm] = React.useState("0");
  const [cw, setCw] = React.useState("0"); // 上下文窗口(K tokens);0=默认200K
  const [effort, setEffort] = React.useState(NONE);
  const [accessProgram, setAccessProgram] = React.useState(NONE);
  const [priority, setPriority] = React.useState("0"); // 轮询顺位;越大越先
  const [poolExclude, setPoolExclude] = React.useState(false);
  const [maxTokens, setMaxTokens] = React.useState("0"); // 单次回复输出上限;0=不发送
  const [retry, setRetry] = React.useState<LLMRetryOverride>(ZERO_OVERRIDE); // 本配置的重试覆盖;全 0=跟随全局
  const [testing, setTesting] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [models, setModels] = React.useState<ChatGPTModel[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsLoaded, setModelsLoaded] = React.useState(false);
  const [modelError, setModelError] = React.useState("");
  const nameEdited = React.useRef(false);
  const accountSnapshot = React.useRef(accounts);
  accountSnapshot.current = accounts;
  const testRequest = React.useRef(0);
  const [testResult, setTestResult] = React.useState<{
    result: Awaited<ReturnType<typeof api.testLLM>>;
    fingerprint: string;
    checkedAt: string;
  } | null>(null);
  const modelRequest = React.useRef(0);
  const isChatGPT = authMode === "chatgpt_plan";
  const selectedAccount = accounts.find((account) => account.id === accountId);
  const accountReady = Boolean(selectedAccount?.connected && selectedAccount.plan_enabled);

  const sameCredentials =
    profile?.auth_mode === authMode &&
    !apiKey &&
    (profile.proxy ?? "") === proxy &&
    (!isChatGPT || profile.chatgpt_account_id === accountId);
  const capabilities =
    models.find((item) => item.id === model)?.capabilities ??
    (profile?.model === model && sameCredentials ? profile.capabilities : undefined);
  const reasoningKnown = capabilities?.reasoning_efforts_known ?? capabilities?.known ?? false;
  const programsKnown = capabilities?.access_programs_known ?? (!isChatGPT && capabilities?.known) ?? false;
  const accountProgramsKnown = isChatGPT && programsKnown && capabilities?.source === "account_catalog";
  const efforts = reasoningKnown
    ? (capabilities?.reasoning_efforts ?? [])
    : ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"];
  const accountCapabilities = capabilities?.source === "account_catalog";
  const effortUnsupported = accountCapabilities && reasoningKnown && effort !== NONE && !efforts.includes(effort);
  const effortChoices = [...new Set([...efforts, ...(effort !== NONE ? [effort] : [])])];
  const accessPrograms = capabilities?.access_programs_cyber ?? [];
  const supportsBlue = accountProgramsKnown && accessPrograms.includes("daybreak_blue");
  const supportsStandard = accountProgramsKnown && accessPrograms.includes("standard");
  const keepSavedStandard =
    profile?.auth_mode === "chatgpt_plan" &&
    profile.access_program_cyber === "standard" &&
    accessProgram === "standard";
  React.useEffect(() => {
    if (supportsStandard && !supportsBlue && accessProgram === NONE) setAccessProgram("standard");
  }, [supportsStandard, supportsBlue, accessProgram]);
  const accessUnsupported = isChatGPT
    ? (accessProgram !== NONE && accessProgram !== "standard") ||
      (accountProgramsKnown && accessProgram === "standard" && !supportsStandard)
    : accountCapabilities && programsKnown && accessProgram !== NONE && !accessPrograms.includes(accessProgram);
  const accessChoices = [
    ...new Set([
      ...(programsKnown ? accessPrograms : ["standard", "daybreak_blue", "daybreak_red"]),
      ...(accessProgram !== NONE ? [accessProgram] : []),
    ]),
  ];
  const outputLimit = isChatGPT ? 0 : Math.max(0, Number(maxTokens) || 0);
  const fingerprint = JSON.stringify([
    authMode,
    accountId,
    accountReady,
    model,
    apiKey,
    proxy,
    effort,
    accessProgram,
    outputLimit,
  ]);
  const currentFingerprint = React.useRef(fingerprint);
  currentFingerprint.current = fingerprint;
  const visibleTest = testResult?.fingerprint === fingerprint ? testResult : null;
  const actualProgram = visibleTest?.result.actual_access_program_cyber ?? "";
  const connected = Boolean(visibleTest?.result.ok);
  const connectionLabel = connected
    ? tf("connectedModel", {
        model: visibleTest?.result.actual_model || visibleTest?.result.model || model,
        program: programLabel(actualProgram, tf),
      })
    : tf("testConnection");
  const credentialsReady = isChatGPT ? accountReady : Boolean(apiKey.trim() || keyHint);
  const profileId = profile ? Number(profile.id) : undefined;
  const catalogIdentity = JSON.stringify([authMode, accountId, apiKey, proxy, profileId]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: 인증 정보가 바뀌면 이전 조회와 진행 중인 응답을 폐기한다.
  React.useEffect(() => {
    modelRequest.current++;
    setModels([]);
    setModelsLoaded(false);
    setLoadingModels(false);
    setModelError("");
  }, [catalogIdentity]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: 설정 변경과 편집창 재열기 모두 이전 연결 결과를 무효화한다.
  React.useEffect(() => {
    testRequest.current++;
    setTestResult(null);
    setTesting(false);
  }, [fingerprint, open]);

  // 每次打开时从传入的 profile 灌一遍表单（新建则重置为默认值）。抽屉关掉再打开
  // 就是一次干净的开始，不会留下上一个配置的残影。
  React.useEffect(() => {
    if (!open) return;
    setName(profile?.name ?? "");
    const readyAccounts = accountSnapshot.current.filter((account) => account.connected && account.plan_enabled);
    setAuthMode(profile?.auth_mode ?? (readyAccounts.length ? "chatgpt_plan" : "api_key"));
    setAccountId(profile?.chatgpt_account_id ?? (readyAccounts.length === 1 ? readyAccounts[0].id : ""));
    nameEdited.current = Boolean(profile);
    setModel(profile?.configuration_error ? "" : (profile?.model ?? ""));
    setProxy(profile?.proxy ?? "");
    setRps(String(profile?.rate_per_second ?? 0));
    setRpm(String(profile?.rate_per_minute ?? 0));
    setCw(String(profile?.context_window_k ?? 0));
    setEffort(fromStore(profile?.reasoning_effort));
    setAccessProgram(fromStore(profile?.access_program_cyber));
    setPriority(String(profile?.priority ?? 0));
    setPoolExclude(profile?.pool_exclude ?? false);
    setMaxTokens(String(profile?.max_tokens ?? 0));
    setRetry(profile?.retry ?? ZERO_OVERRIDE);
    setApiKey("");
    setKeyHint(profile?.api_key_hint ?? "");
    setModels([]);
    setModelsLoaded(false);
    setLoadingModels(false);
    setModelError("");
    modelRequest.current++;
  }, [open, profile]);

  React.useEffect(() => {
    if (!open) modelRequest.current++;
  }, [open]);

  async function loadModels() {
    if (loadingModels || !credentialsReady) return;
    if (isChatGPT && !accountReady) {
      toast.error(tf("connectAndSelectAnAvailableChatGPTAccount"));
      return;
    }
    const request = ++modelRequest.current;
    setLoadingModels(true);
    setModelError("");
    try {
      const r = isChatGPT
        ? { ok: true, models: await api.chatgptModels(accountId), error: undefined }
        : await api.fetchLLMModels(apiKey, proxy, profileId).then((result) => ({
            ...result,
            models: result.model_details ?? result.models?.map((id) => ({ id, name: id })),
          }));
      if (request !== modelRequest.current) return;
      if (r.ok && r.models && r.models.length > 0) {
        setModels(r.models);
        setModelsLoaded(true);
        toast.success(t("profile.toast.modelsLoaded", { n: r.models.length }));
      } else {
        setModels([]);
        setModelsLoaded(true);
        setModelError(r.error ?? tf("noModelsWereReturned"));
      }
    } catch (e) {
      if (request === modelRequest.current) setModelError((e as Error).message);
    } finally {
      if (request === modelRequest.current) setLoadingModels(false);
    }
  }

  function selectModel(id: string) {
    setModel(id);
    const selected = models.find((item) => item.id === id);
    const c = selected?.capabilities;
    if (!nameEdited.current) setName(`${selected?.name || id} · ${isChatGPT ? "ChatGPT" : "API"}`);
    setCw(selected?.context_window ? String(Math.floor(selected.context_window / 1000)) : "0");
    if (isChatGPT && !keepSavedStandard)
      setAccessProgram(
        c?.source === "account_catalog" &&
          c.access_programs_known &&
          c.access_programs_cyber?.includes("standard") &&
          !c.access_programs_cyber.includes("daybreak_blue")
          ? "standard"
          : NONE,
      );
  }

  async function testConnection() {
    if (testing || saving || !model.trim() || !credentialsReady || effortUnsupported || accessUnsupported) return;
    const request = ++testRequest.current;
    const snapshot = fingerprint;
    setTesting(true);
    setTestResult(null);
    try {
      const result = await api.testLLM({
        model: model.trim(),
        api_key: isChatGPT ? "" : apiKey,
        proxy,
        reasoning_effort: toStore(effort),
        access_program_cyber: toStore(accessProgram),
        profile_id: profileId,
        auth_mode: authMode,
        chatgpt_account_id: isChatGPT ? accountId : undefined,
        max_tokens: outputLimit,
      });
      if (request !== testRequest.current || snapshot !== currentFingerprint.current) return;
      setTestResult({ result, fingerprint: snapshot, checkedAt: result.checked_at || new Date().toISOString() });
    } catch (e) {
      if (request === testRequest.current && snapshot === currentFingerprint.current)
        setTestResult({
          result: { ok: false, error: (e as Error).message },
          fingerprint: snapshot,
          checkedAt: new Date().toISOString(),
        });
    } finally {
      if (request === testRequest.current) setTesting(false);
    }
  }

  async function save(useNow = false) {
    if (isChatGPT && !accountReady) {
      toast.error(tf("connectAndSelectAnAvailableChatGPTAccount"));
      return;
    }
    if (!name.trim() || !model.trim()) {
      toast.error(t("profile.toast.nameModelRequired"));
      return;
    }
    if (saving || testing || !credentialsReady || !visibleTest?.result.ok || effortUnsupported || accessUnsupported)
      return;
    setSaving(true);
    try {
      const { id, activated } = await api.saveLLMProfile({
        ...(profile ? { id: Number(profile.id) } : {}),
        name: name.trim(),
        auth_mode: authMode,
        chatgpt_account_id: isChatGPT ? accountId : "",
        model: model.trim(),
        proxy: proxy.trim(),
        api_key: isChatGPT ? "" : apiKey,
        rate_per_second: Number(rps) || 0,
        rate_per_minute: Number(rpm) || 0,
        context_window_k: Number(cw) || 0,
        reasoning_effort: toStore(effort),
        access_program_cyber: toStore(accessProgram),
        connection_check: {
          model: visibleTest?.result.actual_model || visibleTest?.result.model || model.trim(),
          access_program_cyber: actualProgram,
          checked_at: visibleTest.checkedAt,
        },
        priority: isChatGPT ? 0 : Number(priority) || 0,
        pool_exclude: isChatGPT || poolExclude,
        max_tokens: outputLimit,
        // 字段名开关只对 openai(Chat Completions) 有意义，其它格式一律回落到默认；
        // 后端也会再做一次同样的归一化，这里只是别让 UI 送出自相矛盾的值。
        retry,
      });
      if (useNow && !activated && !profile?.is_default) {
        try {
          await api.activateLLMProfile(String(id));
        } catch (e) {
          onSaved(String(id));
          onOpenChange(false);
          toast.error(tf("activationFailed", { error: (e as Error).message }));
          return;
        }
      }
      toast.success(
        activated || useNow || profile?.is_default ? tf("modelSettingsSavedAndActivated") : tf("modelSettingsSaved"),
      );
      onSaved(String(id));
      onOpenChange(false);
    } catch (e) {
      toast.error(t("profile.toast.saveFailed", { msg: (e as Error).message }));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:min-w-[420px] data-[side=right]:sm:max-w-xl"
      >
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            {isNew ? t("profile.titleNew") : t("profile.titleEdit", { name: profile?.name ?? "" })}
            {profile?.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                {t("profile.activeBadge")}
              </Badge>
            )}
          </SheetTitle>
          <SheetDescription>{isNew ? t("profile.descNew") : t("profile.descEdit")}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
          {profile?.configuration_error && (
            <p role="alert" className="text-destructive text-sm">
              {profile.configuration_error}
            </p>
          )}
          <div className="grid gap-2">
            <Label htmlFor="p-auth-mode">{tf("connectionMethod")}</Label>
            <Select
              value={authMode}
              onValueChange={(value) => {
                setAuthMode(value as LLMAuthMode);
                setAccessProgram(NONE);
                if (value === "chatgpt_plan") {
                  const ready = accounts.filter((account) => account.connected && account.plan_enabled);
                  setAccountId(ready.length === 1 ? ready[0].id : "");
                }
                setModel("");
                setModels([]);
                setModelsLoaded(false);
                setLoadingModels(false);
                modelRequest.current++;
              }}
            >
              <SelectTrigger id="p-auth-mode">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="api_key">{tf("openAIAPIKey")}</SelectItem>
                <SelectItem value="chatgpt_plan">{tf("chatGPTSubscription")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {isChatGPT && (
            <div className="grid gap-2 rounded-lg border p-3">
              <Label htmlFor="p-chatgpt-account">{tf("chatGPTAccount")}</Label>
              <Select
                value={accountId || NONE}
                onValueChange={(value) => {
                  setAccountId(toStore(value));
                  if (!keepSavedStandard) setAccessProgram(NONE);
                  setModel("");
                  setModels([]);
                  setModelsLoaded(false);
                  setLoadingModels(false);
                  modelRequest.current++;
                }}
              >
                <SelectTrigger id="p-chatgpt-account">
                  <SelectValue placeholder={tf("selectAnAccount")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>{tf("chooseAnAccount")}</SelectItem>
                  {accounts.map((account) => (
                    <SelectItem
                      key={account.id}
                      value={account.id}
                      disabled={!account.connected || !account.plan_enabled}
                    >
                      {chatGPTAccountLabel(account)}
                      {account.connected && account.plan_enabled ? "" : tf("connectionNeedsVerification")}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {!accountReady && <p className="text-destructive text-xs">{tf("closeTheseSettingsAndSignInThrough")}</p>}
              <p className="text-muted-foreground text-xs">
                {tf("responsesAreStreamedTemperatureAndServerOutputtoken")}
              </p>
              <p className="text-muted-foreground text-xs">{tf("localTimeAndResponsesizeLimitsForSummaries")}</p>
            </div>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="p-name">{t("profile.name.label")}</Label>
              <Input
                id="p-name"
                placeholder={t("profile.name.placeholder")}
                value={name}
                onChange={(e) => {
                  nameEdited.current = true;
                  setName(e.target.value);
                }}
              />
            </div>
          </div>

          {!isChatGPT && (
            <div className="grid gap-2">
              <Label htmlFor="p-api-key">API Key</Label>
              <Input
                id="p-api-key"
                type="password"
                placeholder={keyHint ? t("profile.apiKey.placeholderSet", { hint: keyHint }) : "sk-…"}
                value={apiKey}
                onChange={(e) => {
                  setApiKey(e.target.value);
                  setModel("");
                  setAccessProgram(NONE);
                }}
              />
            </div>
          )}

          <div className="grid gap-2">
            <Label htmlFor="p-model">{tf("model")}</Label>
            {model || modelsLoaded ? (
              <div className="flex gap-2">
                <Select value={model || NONE} onValueChange={(value) => selectModel(toStore(value))}>
                  <SelectTrigger id="p-model" className="min-w-0 flex-1" aria-label={tf("model")}>
                    <SelectValue placeholder={tf("chooseAModel")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE} disabled>
                      {tf("chooseAModel")}
                    </SelectItem>
                    {model && !models.some((item) => item.id === model) && (
                      <SelectItem value={model}>
                        {model} {tf("savedModel")}
                      </SelectItem>
                    )}
                    {models.map((item) => (
                      <SelectItem key={item.id} value={item.id}>
                        {item.name || item.id}
                        {isChatGPT && <Badge variant="outline">{catalogProgramLabel(item, tf)}</Badge>}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  aria-label={tf("refreshModelList")}
                  disabled={loadingModels || !credentialsReady}
                  onClick={() => void loadModels()}
                >
                  <RefreshCwIcon className={loadingModels ? "animate-spin" : ""} />
                </Button>
              </div>
            ) : (
              <Button
                type="button"
                variant="outline"
                disabled={loadingModels || !credentialsReady}
                onClick={() => void loadModels()}
              >
                {loadingModels && <Loader2Icon className="animate-spin" />}
                {isChatGPT ? tf("loadAccountModels") : tf("loadAPIModels")}
              </Button>
            )}
            {loadingModels && (
              <p role="status" className="text-muted-foreground text-xs">
                {tf("loadingAvailableModels")}
              </p>
            )}
            {modelError && (
              <p role="alert" className="text-destructive text-xs">
                {modelError}
              </p>
            )}
            {!isChatGPT && !credentialsReady && (
              <p className="text-muted-foreground text-xs">{tf("enterAnAPIKeyThenLoadModels")}</p>
            )}
            {!isChatGPT && model && (
              <div className="grid gap-2">
                <Button
                  type="button"
                  variant="outline"
                  disabled={testing || saving || !credentialsReady || effortUnsupported || accessUnsupported}
                  onClick={() => void testConnection()}
                >
                  {testing ? <Loader2Icon className="animate-spin" /> : <ZapIcon />}
                  {tf("checkDaybreak")}
                </Button>
                <p className="text-muted-foreground text-xs">
                  {accessProgram === NONE
                    ? tf("selectsAnAvailableProgramAutomaticallyTheConnection")
                    : tf("checkSelectedProgram", { program: programLabel(toStore(accessProgram), tf) })}
                </p>
              </div>
            )}
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className={cn("flex items-center justify-between gap-4", !isChatGPT && "border-t pt-3")}>
              <div className="grid gap-0.5">
                <Label htmlFor="p-effort" className="text-sm">
                  {t("profile.effort.label")}
                </Label>
                <p className="text-muted-foreground text-xs">
                  {capabilities?.default_reasoning_effort
                    ? tf("catalogDefaultEffort", { effort: capabilities.default_reasoning_effort })
                    : t("profile.effort.hint")}
                </p>
              </div>
              <Select value={effort} onValueChange={setEffort}>
                <SelectTrigger id="p-effort" className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>{t("profile.effort.opt.none")}</SelectItem>
                  {effortChoices.map((value) => (
                    <SelectItem key={value} value={value}>
                      {value}
                      {reasoningKnown &&
                        !efforts.includes(value) &&
                        (accountCapabilities ? tf("savedValueIsUnsupported") : tf("savedValueIsUndocumented"))}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <p className="text-muted-foreground text-xs">
            {capabilities?.source === "account_catalog"
              ? tf("capabilitiesComeFromTheSelectedAccountsModel")
              : capabilities?.source === "documentation" || capabilities?.known
                ? tf("modelCapabilitiesComeFromOfficialDocumentationTest")
                : tf("modelCapabilitiesAreUnverifiedNoSupportInformation")}
          </p>
          {effortUnsupported && (
            <p role="alert" className="text-destructive text-xs">
              {tf("thisModelDoesNotSupportTheSaved")}
            </p>
          )}
          {isChatGPT && (
            <div className="grid gap-2 rounded-lg border p-3">
              <Label>{tf("program")}</Label>
              {supportsBlue ? (
                <p className="text-sm">{tf("daybreakSupportedByTheAccountModelCatalog")}</p>
              ) : supportsStandard ? (
                <p className="text-sm">{tf("standardTheAccountModelCatalogOnlyOffers")}</p>
              ) : (
                <p className="text-muted-foreground text-sm">
                  {tf("programSupportIsUnverified")}
                  {accessProgram === "standard" ? tf("savedSettingStandard") : ""}
                </p>
              )}
              {supportsBlue && accessProgram === "standard" && !accessUnsupported && (
                <p className="text-muted-foreground text-xs">
                  {tf("theSavedSettingIsStandard")}{" "}
                  <button type="button" className="underline" onClick={() => setAccessProgram(NONE)}>
                    {tf("switchToAutomaticSelection")}
                  </button>
                </p>
              )}
            </div>
          )}
          {accessUnsupported && (
            <p role="alert" className="text-destructive text-xs">
              {tf("theSavedProgramIsNotSupportedBy")}{" "}
              <button type="button" className="underline" onClick={() => setAccessProgram(NONE)}>
                {tf("switchToAutomaticSelection")}
              </button>
            </p>
          )}
          {visibleTest && (
            <section aria-label={tf("latestConnectionTest")} className="grid gap-1 rounded-lg border p-3 text-sm">
              {visibleTest.result.ok ? (
                <>
                  <p className="font-medium">{connectionLabel}</p>
                  <p className="text-muted-foreground text-xs">
                    {tf("responseTime")}
                    {((visibleTest.result.latency_ms ?? 0) / 1000).toFixed(1)}
                    {tf("s")}
                  </p>
                  {supportsBlue && accessProgram === NONE && actualProgram === "standard" && (
                    <p className="text-muted-foreground text-xs">{tf("theAccountModelCatalogSupportsDaybreakBut")}</p>
                  )}
                </>
              ) : (
                <p role="alert" className="text-destructive">
                  {visibleTest.result.error || tf("couldNotVerifyTheConnection")}
                </p>
              )}
              <p className="text-muted-foreground text-xs">
                {new Date(visibleTest.checkedAt).toLocaleString(getDateLocale())} {tf("checkedResultOfThisRequestOnly")}
              </p>
            </section>
          )}
          <details className="rounded-lg border p-3">
            <summary className="cursor-pointer font-medium text-sm">{tf("advancedSettings")}</summary>
            <div className="mt-4 grid gap-4">
              <div className="grid gap-2">
                <Label htmlFor="p-proxy">{t("profile.proxy.label")}</Label>
                <Input
                  id="p-proxy"
                  className="font-mono"
                  placeholder="socks5://user:pass@127.0.0.1:1080 · http://127.0.0.1:8080"
                  value={proxy}
                  onChange={(e) => setProxy(e.target.value)}
                />
                <p className="text-muted-foreground text-xs">{t("profile.proxy.hint")}</p>
              </div>

              <div className="grid gap-4 sm:grid-cols-3">
                <div className="grid gap-2">
                  <Label htmlFor="p-rps">{t("profile.rps")}</Label>
                  <Input id="p-rps" type="number" min={0} value={rps} onChange={(e) => setRps(e.target.value)} />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="p-rpm">{t("profile.rpm")}</Label>
                  <Input id="p-rpm" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="p-cw">{t("profile.cw")}</Label>
                  <Input
                    id="p-cw"
                    type="number"
                    min={0}
                    max={1000}
                    value={cw}
                    onChange={(e) => setCw(e.target.value)}
                    placeholder="200"
                  />
                </div>
              </div>
              <p className="-mt-2 text-muted-foreground text-xs">{t("profile.rateHint")}</p>

              {!isChatGPT && (
                <div className="grid gap-3 rounded-lg border p-3">
                  <div className="flex items-center justify-between gap-4">
                    <div className="grid gap-0.5">
                      <Label htmlFor="p-priority" className="text-sm">
                        {t("profile.priority.label")}
                      </Label>
                      <p className="text-muted-foreground text-xs">{t("profile.priority.hint")}</p>
                    </div>
                    <Input
                      id="p-priority"
                      type="number"
                      className="w-24 shrink-0"
                      value={priority}
                      onChange={(e) => setPriority(e.target.value)}
                    />
                  </div>
                  <div className="flex items-center justify-between gap-4 border-t pt-3">
                    <div className="grid gap-0.5">
                      <Label className="text-sm">{t("profile.exclude.label")}</Label>
                      <p className="text-muted-foreground text-xs">{t("profile.exclude.hint")}</p>
                    </div>
                    <Switch
                      checked={poolExclude}
                      onCheckedChange={setPoolExclude}
                      aria-label={t("profile.exclude.aria")}
                    />
                  </div>
                </div>
              )}

              {!isChatGPT && (
                <div className="grid gap-3 rounded-lg border p-3">
                  <div className="flex items-center justify-between gap-4">
                    <div className="grid gap-0.5">
                      <Label htmlFor="p-max-tokens" className="text-sm">
                        {t("profile.maxTokens.label")}
                      </Label>
                      <p className="text-muted-foreground text-xs">{t("profile.maxTokens.hint")}</p>
                    </div>
                    <Input
                      id="p-max-tokens"
                      type="number"
                      min={0}
                      className="w-28 shrink-0"
                      value={maxTokens}
                      onChange={(e) => setMaxTokens(e.target.value)}
                      placeholder="0"
                    />
                  </div>
                </div>
              )}

              {!isChatGPT && (
                <>
                  <div className="grid gap-2">
                    <Label htmlFor="p-model-manual">{tf("enterModelIDManually")}</Label>
                    <Input id="p-model-manual" value={model} onChange={(e) => selectModel(e.target.value)} />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="p-access-program">{tf("aPIAccessProgram")}</Label>
                    <Select value={accessProgram} onValueChange={setAccessProgram}>
                      <SelectTrigger id="p-access-program">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={NONE}>{tf("automaticSelectionDefault")}</SelectItem>
                        {accessChoices.map((value) => (
                          <SelectItem key={value} value={value}>
                            {value}
                            {programsKnown &&
                              !accessPrograms.includes(value) &&
                              (accountCapabilities ? tf("savedValueIsUnsupported") : tf("savedValueIsUndocumented"))}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <p className="text-muted-foreground text-xs">{tf("aPIProgramSelectionRequiresModelSupportAnd")}</p>
                  </div>
                </>
              )}
              <ProfileRetryFields value={retry} onChange={setRetry} />
            </div>
          </details>
        </div>

        <div className="flex flex-wrap gap-2 border-t px-4 py-3">
          <Button
            variant="outline"
            className="h-auto min-h-9 w-full whitespace-normal break-all py-2"
            aria-label={tf("testConnection")}
            onClick={() => void testConnection()}
            disabled={testing || saving || !model || !credentialsReady || effortUnsupported || accessUnsupported}
          >
            {testing && <Loader2Icon className="animate-spin" />}
            {!testing && (connected ? <CheckIcon /> : <PlugZapIcon />)}
            {testing ? tf("testingConnection") : connectionLabel}
          </Button>
          <Button
            variant="outline"
            onClick={() => void save()}
            disabled={
              saving ||
              testing ||
              !connected ||
              !name.trim() ||
              !credentialsReady ||
              effortUnsupported ||
              accessUnsupported
            }
          >
            <SaveIcon /> {tf("save")}
          </Button>
          <Button
            className="flex-1"
            onClick={() => void save(true)}
            disabled={
              saving ||
              testing ||
              !connected ||
              !name.trim() ||
              !credentialsReady ||
              effortUnsupported ||
              accessUnsupported
            }
          >
            {saving ? <Loader2Icon className="animate-spin" /> : <PlusIcon />}
            {profile?.is_default ? tf("saveAndApply") : tf("saveAndActivate")}
          </Button>
          {!connected && (
            <p className="w-full text-muted-foreground text-xs">{tf("aSuccessfulConnectionTestIsRequiredBefore")}</p>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────

export default function LLMPage() {
  const t = useTranslations("llmPage");
  const tf = useTranslations("forkUI");
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [accounts, setAccounts] = React.useState<ChatGPTAccount[]>([]);
  const [pool, setPool] = React.useState<LLMPoolStatus | null>(null);
  const [poolOpen, setPoolOpen] = React.useState(false);
  // 抽屉的开关和内容分开存：关闭时 editing 保持不变，否则关闭动画期间标题会从
  // 「编辑 X」闪成「新建」。editing = null 表示新建。
  const [editOpen, setEditOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<LLMProfile | null>(null);
  const openEditor = React.useCallback((p: LLMProfile | null) => {
    setEditing(p);
    setEditOpen(true);
  }, []);

  // 一个配置在卡片上显示的「是否正常」。没填 Key 的配置根本发不出请求，比熔断更该先说。
  const healthOf = React.useCallback(
    (p: LLMProfile, m?: LLMPoolMember): Health => {
      if (
        p.auth_mode === "chatgpt_plan" &&
        (p.plan_blocked || accounts.find((account) => account.id === p.chatgpt_account_id)?.plan_blocked)
      ) {
        return {
          label: tf("usageLimitRecheckRequired"),
          cls: "border-amber-500/50 text-amber-600",
          hint: tf("reviewUsageLimitsAndRunARecheck"),
        };
      }
      if (p.configuration_error)
        return { label: tf("updateConfiguration"), cls: "text-destructive", hint: p.configuration_error };
      if (p.auth_mode === "chatgpt_plan" && !p.connected) {
        return {
          label: tf("chatGPTConnectionRequired"),
          cls: "border-muted-foreground/40 text-muted-foreground",
          hint: tf("checkTheChatGPTAccountConnectionStatus"),
        };
      }
      if (p.auth_mode !== "chatgpt_plan" && !(p.connected ?? p.api_key_hint)) {
        return {
          label: t("health.noKey.label"),
          cls: "border-muted-foreground/40 text-muted-foreground",
          hint: t("health.noKey.hint"),
        };
      }
      if (m?.state === "tripped") {
        return {
          label:
            m.cooldown_secs > 0
              ? t("health.trippedCooldown", { cooldown: cooldownText(m.cooldown_secs) })
              : t("health.tripped"),
          cls: "border-destructive/50 text-destructive",
          hint: m.last_error,
        };
      }
      if (m?.state === "degraded") {
        return {
          label: t("health.degraded", { fails: m.fails }),
          cls: "border-amber-500/50 text-amber-600 dark:text-amber-400",
          hint: m.last_error,
        };
      }
      return {
        label: p.auth_mode === "chatgpt_plan" ? tf("chatGPTConnected") : t("health.ok"),
        cls: "border-emerald-500/50 text-emerald-600 dark:text-emerald-400",
      };
    },
    [t, accounts, tf],
  );

  const loadPool = React.useCallback(async () => {
    try {
      setPool(await api.llmPool());
    } catch {
      /* ignore */
    }
  }, []);

  const load = React.useCallback(async () => {
    try {
      setProfiles(await api.llmProfiles());
    } catch {
      /* ignore */
    }
    await loadPool();
  }, [loadPool]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 卡片上的健康徽章按 profile id 取轮询状态。
  const health = React.useMemo(() => {
    const m = new Map<string, LLMPoolMember>();
    for (const c of pool?.chain ?? []) m.set(c.profile_id, c);
    return m;
  }, [pool]);

  async function activate(id: string, name: string) {
    try {
      await api.activateLLMProfile(id);
      toast.success(t("page.toast.activated", { name }));
      await load();
    } catch (e) {
      toast.error(t("page.toast.activateFailed", { msg: (e as Error).message }));
    }
  }

  async function remove(p: LLMProfile) {
    if (p.is_default) {
      toast.error(t("page.toast.cannotDeleteActive"));
      return;
    }
    try {
      await api.deleteLLMProfile(p.id);
      toast.success(t("page.toast.deleted", { name: p.name }));
      await load();
    } catch (e) {
      toast.error(t("page.toast.deleteFailed", { msg: (e as Error).message }));
    }
  }

  const poolOn = pool?.enabled ?? false;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">LLM</h1>
          <p className="text-muted-foreground text-sm">{t("page.subtitle")}</p>
        </div>
      </div>

      <ChatGPTAccountCard onAccounts={setAccounts} onChanged={load} profiles={profiles} />

      <Tabs defaultValue="profiles" className="flex-1">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <TabsList>
            <TabsTrigger value="profiles">{t("page.tab.profiles")}</TabsTrigger>
            <TabsTrigger value="retry">{t("page.tab.retry")}</TabsTrigger>
          </TabsList>
          <Button size="sm" variant="outline" onClick={() => setPoolOpen(true)}>
            <ZapIcon /> {t("page.poolConfig")}
            {poolOn && (
              <Badge variant="outline" className="ml-1 border-emerald-500/50 text-emerald-600 dark:text-emerald-400">
                {t("page.enabled")}
              </Badge>
            )}
          </Button>
        </div>

        <TabsContent value="profiles" className="mt-4">
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {profiles.map((p) => {
              const h = healthOf(p, health.get(p.id));
              return (
                // biome-ignore lint/a11y/useSemanticElements: 卡片内含自己的操作按钮，用原生 <button> 会造成按钮嵌套（非法 HTML）
                <Card
                  key={p.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => openEditor(p)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      openEditor(p);
                    }
                  }}
                  className={cn(
                    "cursor-pointer gap-0 py-4 outline-none transition-colors hover:border-foreground/30",
                    p.is_default && "border-amber-400/50 bg-amber-400/5",
                  )}
                >
                  <CardContent className="grid gap-2 px-4">
                    <div className="flex items-start gap-2">
                      <StarIcon
                        className={cn(
                          "mt-0.5 size-4 shrink-0",
                          p.is_default ? "fill-amber-400 text-amber-400" : "text-muted-foreground",
                        )}
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="truncate font-medium text-sm">{p.name}</span>
                          <Badge variant="outline" className="uppercase">
                            {p.auth_mode === "chatgpt_plan" ? tf("chatGPTSubscriptionLabel") : "OpenAI API"}
                          </Badge>
                          <Badge variant="outline" className={cn("ml-auto", h.cls)} title={h.hint}>
                            {h.label}
                          </Badge>
                        </div>
                        <div className="mt-1 flex flex-wrap items-center gap-2">
                          <code className="truncate font-mono text-muted-foreground text-xs">{p.model}</code>
                          <Badge
                            variant="outline"
                            title={
                              p.connection_check
                                ? tf("connectionCheckAt", {
                                    model: p.connection_check.model,
                                    time: new Date(p.connection_check.checked_at).toLocaleString(getDateLocale()),
                                  })
                                : tf("testTheConnectionToDisplayTheActual")
                            }
                          >
                            {programLabel(p.connection_check?.access_program_cyber, tf)}
                          </Badge>
                        </div>
                      </div>
                    </div>

                    <div className="flex flex-wrap gap-x-3 gap-y-0.5 pl-6 text-muted-foreground text-xs">
                      {p.auth_mode === "chatgpt_plan" ? (
                        <span>
                          {(() => {
                            const account = accounts.find((item) => item.id === p.chatgpt_account_id);
                            return account ? chatGPTAccountLabel(account) : p.chatgpt_account_id;
                          })()}
                        </span>
                      ) : (
                        p.api_key_hint && <span>{p.api_key_hint}</span>
                      )}
                      <span>
                        {p.rate_per_second}/s · {p.rate_per_minute}/min
                      </span>
                      {p.proxy && <span className="truncate">{t("card.proxy", { proxy: p.proxy })}</span>}
                      {p.reasoning_effort && (
                        <span>
                          {t("card.thinking", {
                            effort: p.reasoning_effort === "off" ? t("card.thinkingOff") : p.reasoning_effort,
                          })}
                        </span>
                      )}
                      {/* 轮询相关的两个字段只在轮询开着时才有意义，关着时不占版面 */}
                      {poolOn &&
                        !p.is_default &&
                        (p.pool_exclude ? (
                          <span>{t("card.excluded")}</span>
                        ) : (
                          <span>{t("card.priority", { priority: p.priority ?? 0 })}</span>
                        ))}
                    </div>

                    <div className="mt-1 flex gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        className="flex-1"
                        disabled={p.is_default || !p.connected || p.plan_blocked || Boolean(p.configuration_error)}
                        onClick={(e) => {
                          e.stopPropagation();
                          void activate(p.id, p.name);
                        }}
                      >
                        {p.is_default ? t("card.active") : t("card.activate")}
                      </Button>
                      <Button
                        size="icon"
                        variant="outline"
                        aria-label={t("card.deleteAria")}
                        onClick={(e) => {
                          e.stopPropagation();
                          void remove(p);
                        }}
                      >
                        <Trash2Icon className="text-destructive" />
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              );
            })}
            {profiles.length === 0 && (
              <div className="col-span-full rounded-lg border border-dashed p-10 text-center text-muted-foreground text-sm">
                {t("page.empty")}
              </div>
            )}
          </div>
          <div className="flex justify-center py-6">
            <Button variant="outline" onClick={() => openEditor(null)} aria-label={tf("addModelSettings")}>
              <PlusIcon /> {tf("addModelSettings")}
            </Button>
          </div>
        </TabsContent>

        <TabsContent value="retry" className="mt-4">
          <RetryPolicyPanel />
        </TabsContent>
      </Tabs>

      <ProfileSheet
        profile={editing}
        accounts={accounts}
        open={editOpen}
        onOpenChange={setEditOpen}
        onSaved={() => void load()}
      />
      <PoolSheet open={poolOpen} onOpenChange={setPoolOpen} pool={pool} onReload={loadPool} />
    </div>
  );
}
