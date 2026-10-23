"use client";

import * as React from "react";

import { useTranslations } from "next-intl";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { PresetCatalog, Settings } from "@/lib/types";

export function ResourcePresets({
  settings,
  onApplied,
  disabled = false,
  onPendingChange,
}: {
  settings: Settings | null;
  onApplied: (settings: Settings) => void;
  disabled?: boolean;
  onPendingChange?: (pending: boolean) => void;
}) {
  const t = useTranslations("presets");
  const [catalog, setCatalog] = React.useState<PresetCatalog | null>(null);
  const [error, setError] = React.useState("");
  const [pending, setPending] = React.useState("");
  const alive = React.useRef(true);
  const busy = React.useRef(false);
  React.useEffect(() => {
    alive.current = true;
    const controller = new AbortController();
    api
      .presets(controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setCatalog(value);
      })
      .catch((err) => {
        if (!controller.signal.aborted) setError((err as Error).message);
      });
    return () => {
      alive.current = false;
      controller.abort();
    };
  }, []);

  const apply = async (id: string) => {
    if (busy.current || disabled) return;
    busy.current = true;
    setPending(id);
    onPendingChange?.(true);
    setError("");
    try {
      const value = await api.applyResourcePreset(id);
      if (alive.current) onApplied(value);
    } catch (err) {
      if (alive.current) setError((err as Error).message);
      // Runtime reconfiguration can fail after the settings transaction commits.
      try {
        const value = await api.settings();
        if (alive.current) onApplied(value);
      } catch {
        /* Keep the last known settings if refresh also fails. */
      }
    } finally {
      busy.current = false;
      if (alive.current) {
        setPending("");
        onPendingChange?.(false);
      }
    }
  };

  return (
    <section aria-label={t("resourcesTitle")} className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 font-medium text-sm">{t("resourcesTitle")}</span>
        {catalog?.resources.map((preset) => (
          <Button
            key={preset.id}
            size="sm"
            variant={settings?.resource_preset === preset.id ? "default" : "outline"}
            className="capitalize"
            aria-pressed={settings?.resource_preset === preset.id}
            aria-busy={pending === preset.id}
            title={t("counts", { workers: preset.workers, tasks: preset.tasks, browsers: preset.browsers })}
            disabled={disabled === true || settings === null || pending !== ""}
            onClick={() => void apply(preset.id)}
          >
            {preset.id}
          </Button>
        ))}
        <Badge variant="secondary">
          {t("current", {
            name: settings?.resource_preset === "custom" ? t("custom") : (settings?.resource_preset ?? t("loading")),
          })}
        </Badge>
        {pending && (
          <span role="status" className="text-muted-foreground text-sm">
            {t("applying")}
          </span>
        )}
      </div>
      <p className="text-muted-foreground text-xs">
        {t("compactSummary")}
        {settings && (
          <>
            {" "}
            {t("effective", {
              workers: settings.workers ?? 3,
              tasks: settings.task_concurrency_enabled ? String(settings.task_concurrency_limit) : t("unlimited"),
              browsers: settings.browser_concurrency_limit ?? 4,
            })}
          </>
        )}
      </p>
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
    </section>
  );
}
