"use client";

import { useTranslations } from "next-intl";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import type { Task } from "@/lib/types";

export function TaskRuntimeSummary({ task }: { task: Task }) {
  const t = useTranslations("taskDetail.runtime");
  const runtime = task.runtime;
  if (!runtime) return null;
  const flag = (value: boolean | null) => t(value == null ? "unknown" : value ? "enabled" : "disabled");
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t("title")}</CardTitle>
        <CardDescription>{t("hint")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        <dl className="grid gap-2 sm:grid-cols-2">
          <div>
            <dt className="text-muted-foreground">{t("workers")}</dt>
            <dd>{t(runtime.worker_source, { count: runtime.workers })}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("browserLimit")}</dt>
            <dd>{runtime.browser_limit}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("browser")}</dt>
            <dd>
              {t("browserState", {
                enabled: flag(runtime.browser_enabled),
                visible: flag(runtime.browser_worker_visible),
              })}
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("capture")}</dt>
            <dd>{flag(runtime.traffic_capture)}</dd>
          </div>
        </dl>
        <p className="text-muted-foreground text-xs">{t("browserHint")}</p>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs">
            <thead>
              <tr>
                <th className="py-1 pr-3">{t("role")}</th>
                <th className="pr-3">{t("model")}</th>
                <th className="pr-3">{t("reasoning")}</th>
                <th>{t("program")}</th>
              </tr>
            </thead>
            <tbody>
              {(["mainagent", "planner", "worker"] as const).map((role) => {
                const model = runtime.models[role];
                return (
                  <tr key={role}>
                    <td className="py-1 pr-3">{t(role)}</td>
                    <td className="pr-3">
                      {model?.model || t("unknown")}
                      {model && !model.available && ` · ${t("unavailable")}`}
                    </td>
                    <td className="pr-3">{model ? model.reasoning_effort || t("default") : t("unknown")}</td>
                    <td>{model ? model.requested_program || t("default") : t("unknown")}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {!!runtime.worker_progress?.length && (
          <div>
            <p className="font-medium">{t("activeWorkers")}</p>
            <ul className="mt-1 space-y-1 text-muted-foreground text-xs">
              {runtime.worker_progress.map((progress) => (
                <li key={progress.intent_id}>
                  {progress.worker} · #{progress.intent_id} ·{" "}
                  {t.has(`phase.${progress.phase}`)
                    ? t(`phase.${progress.phase}` as "phase.preparing")
                    : progress.phase}
                  {progress.tool && ` · ${progress.tool}`}
                  {` · ${t("sinceSignal", { seconds: progress.idle_seconds })}`}
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
