"use client";

import * as React from "react";

import { useTranslations } from "next-intl";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { PresetCatalog, Settings } from "@/lib/types";

export function ResourcePresets({
  settings,
  onApplied,
  onAdvanced,
}: {
  settings: Settings | null;
  onApplied: (settings: Settings) => void;
  onAdvanced: () => void;
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
    if (busy.current) return;
    busy.current = true;
    setPending(id);
    setError("");
    try {
      const value = await api.applyResourcePreset(id);
      onApplied(value);
      if (alive.current && id === "custom") onAdvanced();
    } catch (err) {
      if (alive.current) setError((err as Error).message);
      // Runtime reconfiguration can fail after the settings transaction commits.
      try {
        onApplied(await api.settings());
      } catch {
        /* Keep the last known settings if refresh also fails. */
      }
    } finally {
      busy.current = false;
      if (alive.current) setPending("");
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("resourcesTitle")}</CardTitle>
        <CardDescription>{t("resourcesDescription")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap gap-2 text-sm">
          <Badge variant="secondary">
            {t("current", {
              name: settings?.resource_preset === "custom" ? t("custom") : (settings?.resource_preset ?? t("loading")),
            })}
          </Badge>
          {settings && (
            <span>
              {t("effective", {
                workers: settings.workers ?? 3,
                tasks: settings.task_concurrency_enabled ? String(settings.task_concurrency_limit) : t("unlimited"),
                browsers: settings.browser_concurrency_limit ?? 4,
              })}
            </span>
          )}
        </div>
        <div className="grid gap-3 md:grid-cols-3">
          {catalog?.resources.map((preset) => (
            <div key={preset.id} className="space-y-3 rounded-lg border p-4">
              <div className="flex items-center justify-between font-semibold">
                <span className="capitalize">{preset.id}</span>
                {settings?.resource_preset === preset.id && <Badge>{t("applied")}</Badge>}
              </div>
              <p className="text-sm">
                {t("counts", { workers: preset.workers, tasks: preset.tasks, browsers: preset.browsers })}
              </p>
              <ul className="space-y-1 text-xs text-muted-foreground">
                {catalog.tools
                  .filter((tool) => tool.min_tier <= preset.tier)
                  .map((tool) => (
                    <li key={`${tool.kind}:${tool.key}`}>
                      {t.has(`tools.${tool.key}`) ? t(`tools.${tool.key}`) : tool.key}
                    </li>
                  ))}
              </ul>
              <Button
                className="w-full"
                variant={settings?.resource_preset === preset.id ? "secondary" : "default"}
                disabled={!settings || !!pending}
                onClick={() => void apply(preset.id)}
              >
                {pending === preset.id ? t("applying") : t("apply")}
              </Button>
            </div>
          ))}
        </div>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <p className="text-sm text-muted-foreground">{t("applicationScope")}</p>
        <Button variant="outline" disabled={!settings || !!pending} onClick={() => void apply("custom")}>
          {t("customAction")}
        </Button>
      </CardContent>
    </Card>
  );
}
