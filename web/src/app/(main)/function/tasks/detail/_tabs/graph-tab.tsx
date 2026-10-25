"use client";

import * as React from "react";

import { useTranslations } from "next-intl";

import { ExplorationGraph } from "@/components/exploration-graph";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { Edge, ExplorationGraphPage, TaskNode } from "@/lib/types";

export function GraphTab({ taskId }: { taskId: string }) {
  return <GraphWindow key={taskId} taskId={taskId} />;
}

function GraphWindow({ taskId }: { taskId: string }) {
  const t = useTranslations("taskDetail.graphTab");
  const [graph, setGraph] = React.useState<ExplorationGraphPage>({ nodes: [], edges: [] });
  const [pages, setPages] = React.useState(1);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(false);
  const sigRef = React.useRef("");

  React.useEffect(() => {
    let cancelled = false;
    let loading = false;
    const load = async () => {
      if (loading) return;
      loading = true;
      setBusy(true);
      try {
        // Re-read the loaded window so deleted nodes and reopened source intents
        // disappear. Merging only the newest page would retain stale graph state.
        const nodes = new Map<string, TaskNode>();
        const edges = new Map<string, Edge>();
        let before: string | undefined;
        let first: ExplorationGraphPage | undefined;
        let last: ExplorationGraphPage | undefined;
        for (let page = 0; page < pages; page++) {
          const data = await api.explorationGraph(taskId, { before, limit: 500 });
          if (cancelled) return;
          first ??= data;
          last = data;
          for (const node of data.nodes ?? []) nodes.set(node.id, node);
          for (const edge of data.edges ?? []) edges.set(JSON.stringify([edge.src, edge.rel, edge.dst]), edge);
          if (!data.has_more || !data.next_before || data.next_before === before) break;
          before = data.next_before;
        }
        const next: ExplorationGraphPage = {
          nodes: [...nodes.values()],
          edges: [...edges.values()],
          total_nodes: first?.total_nodes ?? nodes.size,
          total_edges: first?.total_edges ?? edges.size,
          has_more: last?.has_more ?? false,
        };
        const sig = JSON.stringify(next);
        if (sig !== sigRef.current) {
          sigRef.current = sig;
          setGraph(next);
        }
        setError(false);
      } catch {
        if (!cancelled) setError(true); // Keep the last complete window on failure.
      } finally {
        loading = false;
        if (!cancelled) setBusy(false);
      }
    };
    void load();
    const timer = setInterval(() => void load(), 20000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [taskId, pages]);

  return (
    <Card>
      <CardContent className="p-0">
        <div className="flex flex-wrap items-center gap-3 border-b px-4 py-3 text-sm">
          <span aria-live="polite">
            {t("counts", {
              nodes: graph.nodes.length,
              totalNodes: graph.total_nodes ?? graph.nodes.length,
              edges: graph.edges.length,
              totalEdges: graph.total_edges ?? graph.edges.length,
            })}
          </span>
          {graph.has_more && (
            <Button size="sm" variant="outline" disabled={busy} onClick={() => setPages((n) => n + 1)}>
              {busy ? t("loading") : t("loadMore")}
            </Button>
          )}
          {error && <span role="alert">{t("error")}</span>}
          <p className="w-full text-muted-foreground text-xs">{t("hint")}</p>
        </div>
        <ExplorationGraph nodes={graph.nodes} edges={graph.edges} className="h-[72vh]" />
      </CardContent>
    </Card>
  );
}
