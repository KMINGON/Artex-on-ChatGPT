"use client";

import * as React from "react";

import { Combobox as ComboboxPrimitive } from "@base-ui/react/combobox";
import { LibraryIcon, PlusIcon, SaveIcon, Settings2Icon, Trash2Icon } from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { AssetInterceptRulesEditor } from "@/components/asset-intercept-rules-editor";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Combobox,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
} from "@/components/ui/combobox";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { AssetInterceptRuleInput, TaskCategory, TaskTemplate } from "@/lib/types";
import { cn } from "@/lib/utils";

export function taskSecondsFromMinutes(value: string | number, minimumSeconds: number): number {
  const seconds = Number(value) * 60;
  return Number.isFinite(seconds) ? Math.max(minimumSeconds, Math.round(seconds)) : minimumSeconds;
}

interface TemplateDraft {
  name: string;
  taskName: string | null;
  timeoutSeconds: number | null;
  planHeartbeatSeconds: number | null;
  seedFirstIntent: boolean | null;
  coverageEnabled: boolean | null;
  description: string;
  goal: string;
  categoryID: number | null;
  interceptRules: AssetInterceptRuleInput[];
}

// 템플릿 이름과 실제 작업 이름은 별도로 저장한다.
type TemplateSeed = Omit<TemplateDraft, "name">;

interface TaskTemplateManagerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  templates: TaskTemplate[];
  seed: TemplateSeed | null;
  onCreated: (template: TaskTemplate) => void;
  onUpdated: (template: TaskTemplate) => void;
  onDeleted: (id: number) => void;
}

const emptyDraft = (): TemplateDraft => ({
  name: "",
  taskName: null,
  timeoutSeconds: null,
  planHeartbeatSeconds: null,
  seedFirstIntent: null,
  coverageEnabled: null,
  description: "",
  goal: "",
  categoryID: null,
  interceptRules: [],
});

function templateDraft(template: TaskTemplate): TemplateDraft {
  return {
    name: template.name,
    taskName: template.task_name ?? null,
    timeoutSeconds: template.timeout_seconds ?? null,
    planHeartbeatSeconds: template.plan_heartbeat_seconds ?? null,
    seedFirstIntent: template.seed_first_intent ?? null,
    coverageEnabled: template.coverage_enabled ?? null,
    description: template.description,
    goal: template.goal,
    categoryID: template.category_id ?? null,
    interceptRules: template.intercept_rules ?? [],
  };
}

function TaskTemplateManager({
  open,
  onOpenChange,
  templates,
  seed,
  onCreated,
  onUpdated,
  onDeleted,
}: TaskTemplateManagerProps) {
  const t = useTranslations("taskTemplate");
  const [selectedID, setSelectedID] = React.useState<number | null>(null);
  const [draft, setDraft] = React.useState<TemplateDraft>(emptyDraft);
  const [saving, setSaving] = React.useState(false);
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);
  const [categories, setCategories] = React.useState<TaskCategory[]>([]);
  const wasOpen = React.useRef(false);

  React.useEffect(() => {
    if (!open) return;
    api
      .taskCategories()
      .then(setCategories)
      .catch(() => setCategories([]));
  }, [open]);

  React.useEffect(() => {
    if (open && !wasOpen.current) {
      if (seed) {
        setSelectedID(null);
        setDraft({
          name: "",
          ...seed,
        });
      } else if (templates[0]) {
        setSelectedID(templates[0].id);
        setDraft(templateDraft(templates[0]));
      } else {
        setSelectedID(null);
        setDraft(emptyDraft());
      }
    }
    wasOpen.current = open;
  }, [open, seed, templates]);

  const selectTemplate = (template: TaskTemplate) => {
    setSelectedID(template.id);
    setDraft(templateDraft(template));
  };

  const startNew = () => {
    setSelectedID(null);
    setDraft(emptyDraft());
  };

  const updateDraft = (field: "name" | "description" | "goal", value: string) => {
    setDraft((current) => ({ ...current, [field]: value }));
  };

  const patchDraft = (patch: Partial<TemplateDraft>) => {
    setDraft((current) => ({ ...current, ...patch }));
  };

  async function save() {
    const input = {
      name: draft.name.trim(),
      task_name: draft.taskName == null ? null : draft.taskName.trim(),
      timeout_seconds: draft.timeoutSeconds == null ? null : taskSecondsFromMinutes(draft.timeoutSeconds / 60, 0),
      plan_heartbeat_seconds:
        draft.planHeartbeatSeconds == null ? null : taskSecondsFromMinutes(draft.planHeartbeatSeconds / 60, 600),
      seed_first_intent: draft.seedFirstIntent,
      coverage_enabled: draft.coverageEnabled,
      description: draft.description.trim(),
      goal: draft.goal.trim(),
      category_id: draft.categoryID,
      intercept_rules: draft.interceptRules
        .map((r) => ({ ...r, pattern: r.pattern.trim() }))
        .filter((r) => r.pattern !== ""),
    };
    if (!input.name || !input.description || !input.goal) {
      toast.error(t("manager.validation"));
      return;
    }
    setSaving(true);
    try {
      if (selectedID == null) {
        const created = await api.createTaskTemplate(input);
        onCreated(created);
        setSelectedID(created.id);
        setDraft(templateDraft(created));
        toast.success(t("manager.created"));
      } else {
        const updated = await api.updateTaskTemplate(selectedID, input);
        onUpdated(updated);
        setDraft(templateDraft(updated));
        toast.success(t("manager.updated"));
      }
    } catch (error) {
      toast.error(t("manager.saveError", { error: (error as Error).message }));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (selectedID == null) return;
    const deletedID = selectedID;
    setDeleting(true);
    try {
      await api.deleteTaskTemplate(deletedID);
      onDeleted(deletedID);
      const next = templates.find((template) => template.id !== deletedID);
      if (next) {
        setSelectedID(next.id);
        setDraft(templateDraft(next));
      } else {
        startNew();
      }
      setDeleteOpen(false);
      toast.success(t("manager.deleted"));
    } catch (error) {
      toast.error(t("manager.deleteError", { error: (error as Error).message }));
    } finally {
      setDeleting(false);
    }
  }

  let saveLabel = saving ? t("manager.saving") : t("manager.saveChanges");
  if (!saving && selectedID == null) saveLabel = t("manager.create");

  return (
    <>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent className="grid h-full w-full! max-w-none! grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:w-[48rem]! sm:max-w-[48rem]!">
          <SheetHeader className="border-b px-6 py-5">
            <SheetTitle>{t("manager.title")}</SheetTitle>
            <SheetDescription>{t("manager.description")}</SheetDescription>
          </SheetHeader>
          <div className="grid min-h-0 overflow-y-auto lg:grid-cols-[15rem_minmax(0,1fr)] lg:overflow-hidden">
            <div className="flex min-h-0 flex-col border-b p-3 lg:border-r lg:border-b-0">
              <Button type="button" variant="outline" className="w-full" onClick={startNew}>
                <PlusIcon data-icon="inline-start" />
                {t("manager.new")}
              </Button>
              <ScrollArea className="mt-2 max-h-44 lg:max-h-none lg:flex-1">
                <div className="flex flex-col gap-1 pr-2">
                  {templates.length === 0 && (
                    <p className="px-2 py-6 text-center text-muted-foreground text-sm">{t("manager.noTemplates")}</p>
                  )}
                  {templates.map((template) => (
                    <button
                      key={template.id}
                      type="button"
                      className={cn(
                        "min-w-0 rounded-md px-2.5 py-2 text-left transition-colors",
                        selectedID === template.id ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
                      )}
                      onClick={() => selectTemplate(template)}
                    >
                      <span className="block truncate font-medium text-sm">{template.name}</span>
                      <span className="block truncate text-muted-foreground text-xs">{template.description}</span>
                    </button>
                  ))}
                </div>
              </ScrollArea>
            </div>
            <ScrollArea className="min-h-0">
              <FieldGroup className="p-6">
                <Field>
                  <FieldLabel htmlFor="task-template-name">{t("manager.nameLabel")}</FieldLabel>
                  <Input
                    id="task-template-name"
                    value={draft.name}
                    maxLength={120}
                    placeholder={t("manager.namePlaceholder")}
                    onChange={(event) => updateDraft("name", event.target.value)}
                  />
                </Field>
                <Field>
                  <div className="flex items-center gap-2">
                    <Checkbox
                      id="task-template-set-task-name"
                      checked={draft.taskName != null}
                      onCheckedChange={(checked) => patchDraft({ taskName: checked ? "" : null })}
                    />
                    <FieldLabel htmlFor="task-template-set-task-name">{t("manager.setTaskName")}</FieldLabel>
                  </div>
                  <Input
                    aria-label={t("manager.taskNameLabel")}
                    value={draft.taskName ?? ""}
                    disabled={draft.taskName == null}
                    placeholder={t("manager.taskNameLabel")}
                    onChange={(event) => patchDraft({ taskName: event.target.value })}
                  />
                  <FieldDescription>{t("manager.taskNameHint")}</FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-description">{t("manager.descriptionLabel")}</FieldLabel>
                  <Textarea
                    id="task-template-description"
                    className="min-h-28"
                    value={draft.description}
                    placeholder={t("manager.descriptionPlaceholder")}
                    onChange={(event) => updateDraft("description", event.target.value)}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-goal">{t("manager.goalLabel")}</FieldLabel>
                  <Textarea
                    id="task-template-goal"
                    className="min-h-28"
                    value={draft.goal}
                    placeholder={t("manager.goalPlaceholder")}
                    onChange={(event) => updateDraft("goal", event.target.value)}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-category">{t("manager.categoryLabel")}</FieldLabel>
                  <NativeSelect
                    id="task-template-category"
                    className="w-full"
                    value={draft.categoryID == null ? "" : String(draft.categoryID)}
                    onChange={(event) =>
                      patchDraft({ categoryID: event.target.value === "" ? null : Number(event.target.value) })
                    }
                  >
                    <NativeSelectOption value="">{t("manager.categoryUnclassified")}</NativeSelectOption>
                    {categories.map((c) => (
                      <NativeSelectOption key={c.id} value={String(c.id)}>
                        {c.name}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                  <FieldDescription>{t("manager.categoryHint")}</FieldDescription>
                </Field>
                <Field>
                  <FieldLabel>{t("manager.rulesLabel")}</FieldLabel>
                  <AssetInterceptRulesEditor
                    value={draft.interceptRules}
                    onChange={(rules) => patchDraft({ interceptRules: rules })}
                  />
                  <FieldDescription>{t("manager.rulesHint")}</FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-timeout">{t("manager.timeoutLabel")}</FieldLabel>
                  <Input
                    id="task-template-timeout"
                    type="number"
                    min={0}
                    step="any"
                    value={draft.timeoutSeconds == null ? "" : draft.timeoutSeconds / 60}
                    placeholder={t("manager.keepCurrent")}
                    onChange={(event) =>
                      patchDraft({ timeoutSeconds: event.target.value === "" ? null : Number(event.target.value) * 60 })
                    }
                  />
                  <FieldDescription>{t("manager.timeoutHint")}</FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-heartbeat">{t("manager.heartbeatLabel")}</FieldLabel>
                  <Input
                    id="task-template-heartbeat"
                    type="number"
                    min={10}
                    step="any"
                    value={draft.planHeartbeatSeconds == null ? "" : draft.planHeartbeatSeconds / 60}
                    placeholder={t("manager.keepCurrent")}
                    onChange={(event) =>
                      patchDraft({
                        planHeartbeatSeconds: event.target.value === "" ? null : Number(event.target.value) * 60,
                      })
                    }
                  />
                  <FieldDescription>{t("manager.heartbeatHint")}</FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-seed">{t("manager.seedLabel")}</FieldLabel>
                  <NativeSelect
                    id="task-template-seed"
                    value={draft.seedFirstIntent == null ? "" : String(draft.seedFirstIntent)}
                    onChange={(event) =>
                      patchDraft({ seedFirstIntent: event.target.value === "" ? null : event.target.value === "true" })
                    }
                  >
                    <NativeSelectOption value="">{t("manager.keepCurrent")}</NativeSelectOption>
                    <NativeSelectOption value="true">{t("manager.enabled")}</NativeSelectOption>
                    <NativeSelectOption value="false">{t("manager.disabled")}</NativeSelectOption>
                  </NativeSelect>
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-coverage">{t("manager.coverageLabel")}</FieldLabel>
                  <NativeSelect
                    id="task-template-coverage"
                    value={draft.coverageEnabled == null ? "" : String(draft.coverageEnabled)}
                    onChange={(event) =>
                      patchDraft({ coverageEnabled: event.target.value === "" ? null : event.target.value === "true" })
                    }
                  >
                    <NativeSelectOption value="">{t("manager.keepCurrent")}</NativeSelectOption>
                    <NativeSelectOption value="true">{t("manager.enabled")}</NativeSelectOption>
                    <NativeSelectOption value="false">{t("manager.disabled")}</NativeSelectOption>
                  </NativeSelect>
                </Field>
              </FieldGroup>
            </ScrollArea>
          </div>
          <SheetFooter className="border-t px-6 py-4 sm:flex-row sm:items-center">
            {selectedID != null && (
              <Button type="button" variant="destructive" className="sm:mr-auto" onClick={() => setDeleteOpen(true)}>
                <Trash2Icon data-icon="inline-start" />
                {t("manager.delete")}
              </Button>
            )}
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("manager.close")}
            </Button>
            <Button type="button" disabled={saving} onClick={() => void save()}>
              {saving ? <Spinner data-icon="inline-start" /> : <SaveIcon data-icon="inline-start" />}
              {saveLabel}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("deleteDialog.title", { name: draft.name || t("deleteDialog.untitled") })}
            </AlertDialogTitle>
            <AlertDialogDescription>{t("deleteDialog.description")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>{t("deleteDialog.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={deleting}
              onClick={(event) => {
                event.preventDefault();
                void remove();
              }}
            >
              {deleting && <Spinner data-icon="inline-start" />}
              {deleting ? t("deleteDialog.deleting") : t("deleteDialog.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

interface TaskTemplateControlsProps {
  taskName: string;
  timeoutSeconds: number;
  planHeartbeatSeconds: number;
  seedFirstIntent: boolean;
  coverageEnabled: boolean;
  description: string;
  goal: string;
  categoryID?: number;
  interceptRules?: AssetInterceptRuleInput[];
  selectedTemplateID: number | null;
  onSelectedTemplateIDChange: (id: number | null) => void;
  onApply: (template: TaskTemplate) => void;
  portalContainer?: React.RefObject<HTMLElement | null>;
}

export function TaskTemplateControls({
  taskName,
  timeoutSeconds,
  planHeartbeatSeconds,
  seedFirstIntent,
  coverageEnabled,
  description,
  goal,
  categoryID,
  interceptRules = [],
  selectedTemplateID,
  onSelectedTemplateIDChange,
  onApply,
  portalContainer,
}: TaskTemplateControlsProps) {
  const t = useTranslations("taskTemplate");
  const { contains } = ComboboxPrimitive.useFilter();
  const [templates, setTemplates] = React.useState<TaskTemplate[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [templateInputValue, setTemplateInputValue] = React.useState("");
  const [pendingTemplate, setPendingTemplate] = React.useState<TaskTemplate | null>(null);
  const [managerOpen, setManagerOpen] = React.useState(false);
  const [managerSeed, setManagerSeed] = React.useState<TemplateSeed | null>(null);

  const loadTemplates = React.useCallback(async () => {
    setLoading(true);
    try {
      setTemplates(await api.taskTemplates());
    } catch (error) {
      setTemplates([]);
      toast.error(t("picker.loadError", { error: (error as Error).message }));
    } finally {
      setLoading(false);
    }
  }, [t]);

  React.useEffect(() => {
    void loadTemplates();
  }, [loadTemplates]);

  const selectedTemplate = React.useMemo(
    () => templates.find((template) => template.id === selectedTemplateID) ?? null,
    [selectedTemplateID, templates],
  );

  React.useEffect(() => {
    setTemplateInputValue(selectedTemplate?.name ?? "");
  }, [selectedTemplate]);

  const applyTemplate = (template: TaskTemplate) => {
    onApply(template);
    onSelectedTemplateIDChange(template.id);
    setPendingTemplate(null);
  };

  const chooseTemplate = (template: TaskTemplate | null) => {
    if (!template) {
      setTemplateInputValue("");
      onSelectedTemplateIDChange(null);
      return;
    }
    setTemplateInputValue(template.name);
    const hasContent =
      taskName.trim() !== "" ||
      description.trim() !== "" ||
      goal.trim() !== "" ||
      categoryID != null ||
      interceptRules.length > 0 ||
      timeoutSeconds !== 0 ||
      planHeartbeatSeconds !== 600 ||
      seedFirstIntent ||
      !coverageEnabled;
    const changesContent =
      description !== template.description ||
      goal !== template.goal ||
      (categoryID ?? null) !== (template.category_id ?? null) ||
      JSON.stringify(interceptRules) !== JSON.stringify(template.intercept_rules ?? []) ||
      (template.task_name != null && taskName !== template.task_name) ||
      (template.timeout_seconds != null && timeoutSeconds !== template.timeout_seconds) ||
      (template.plan_heartbeat_seconds != null && planHeartbeatSeconds !== template.plan_heartbeat_seconds) ||
      (template.seed_first_intent != null && seedFirstIntent !== template.seed_first_intent) ||
      (template.coverage_enabled != null && coverageEnabled !== template.coverage_enabled);
    if (hasContent && changesContent) {
      setPendingTemplate(template);
      return;
    }
    applyTemplate(template);
  };

  const openManager = (seed: TemplateSeed | null) => {
    setManagerSeed(seed);
    setManagerOpen(true);
  };

  const upsertTemplate = (template: TaskTemplate) => {
    setTemplates((current) => [template, ...current.filter((item) => item.id !== template.id)]);
  };

  const deleteTemplate = (id: number) => {
    setTemplates((current) => current.filter((template) => template.id !== id));
    if (selectedTemplateID === id) onSelectedTemplateIDChange(null);
  };

  let pickerPlaceholder = loading ? t("picker.loading") : t("picker.empty");
  if (!loading && templates.length > 0) pickerPlaceholder = t("picker.search");

  return (
    <>
      <Field>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FieldLabel htmlFor="task-template-picker">{t("picker.label")}</FieldLabel>
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => openManager(null)}>
              <Settings2Icon data-icon="inline-start" />
              {t("picker.manage")}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={!description.trim() || !goal.trim()}
              onClick={() =>
                openManager({
                  description,
                  goal,
                  taskName,
                  timeoutSeconds,
                  planHeartbeatSeconds,
                  seedFirstIntent,
                  coverageEnabled,
                  categoryID: categoryID ?? null,
                  interceptRules,
                })
              }
            >
              <SaveIcon data-icon="inline-start" />
              {t("picker.saveAs")}
            </Button>
          </div>
        </div>
        <Combobox
          items={templates}
          // Closing animation can freeze the library query during a rapid reopen.
          filteredItems={templates.filter((template) => contains(template.name, templateInputValue.trim()))}
          itemToStringLabel={(template) => template.name}
          itemToStringValue={(template) => String(template.id)}
          inputValue={templateInputValue}
          onInputValueChange={(value) => setTemplateInputValue(value)}
          value={selectedTemplate}
          onValueChange={chooseTemplate}
        >
          <ComboboxInput
            id="task-template-picker"
            className="w-full"
            placeholder={pickerPlaceholder}
            disabled={loading || templates.length === 0}
            showClear
          />
          <ComboboxContent portalContainer={portalContainer}>
            <ComboboxEmpty>{t("picker.noMatch")}</ComboboxEmpty>
            <ComboboxList>
              {(template) => (
                <ComboboxItem key={template.id} value={template}>
                  <LibraryIcon />
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-medium text-sm">{template.name}</p>
                    {template.description && (
                      <p className="truncate text-muted-foreground text-xs">{template.description}</p>
                    )}
                  </div>
                </ComboboxItem>
              )}
            </ComboboxList>
          </ComboboxContent>
        </Combobox>
        <FieldDescription>{t("picker.hint")}</FieldDescription>
      </Field>

      <AlertDialog
        open={pendingTemplate != null}
        onOpenChange={(open) => {
          if (open) return;
          setPendingTemplate(null);
          setTemplateInputValue(selectedTemplate?.name ?? "");
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("apply.title", { name: pendingTemplate?.name ?? "" })}</AlertDialogTitle>
            <AlertDialogDescription>{t("apply.description")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("apply.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => pendingTemplate && applyTemplate(pendingTemplate)}>
              {t("apply.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <TaskTemplateManager
        open={managerOpen}
        onOpenChange={setManagerOpen}
        templates={templates}
        seed={managerSeed}
        onCreated={upsertTemplate}
        onUpdated={upsertTemplate}
        onDeleted={deleteTemplate}
      />
    </>
  );
}
