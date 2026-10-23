"use client";

import * as React from "react";

import { useLocale, useTranslations } from "next-intl";

import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { TestPreset } from "@/lib/types";

export function TestPresetPicker({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  const t = useTranslations("presets");
  const locale = useLocale();
  const id = React.useId();
  const [presets, setPresets] = React.useState<TestPreset[]>([]);
  const [error, setError] = React.useState("");
  React.useEffect(() => {
    const controller = new AbortController();
    api
      .presets(controller.signal)
      .then((catalog) => {
        if (!controller.signal.aborted) setPresets(catalog.policies);
      })
      .catch((err) => {
        if (!controller.signal.aborted) setError((err as Error).message);
      });
    return () => controller.abort();
  }, []);
  const selected = presets.find((preset) => preset.id === value);
  const selectedDescription = locale === "en" ? selected?.description_en : selected?.description;
  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{t("taskTitle")}</Label>
      <select
        id={id}
        className="w-full rounded-md border bg-background p-2 text-sm"
        disabled={disabled}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        <option value="">{t("taskCustom")}</option>
        {value && !selected && <option value={value}>{value}</option>}
        {presets.map((preset) => (
          <option key={preset.id} value={preset.id}>
            {locale === "en" ? preset.name_en : preset.name}
          </option>
        ))}
      </select>
      <p className="text-xs text-muted-foreground">{selectedDescription ?? t("taskDescription")}</p>
      {selected && <p className="text-xs text-muted-foreground">{t("taskScope")}</p>}
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
