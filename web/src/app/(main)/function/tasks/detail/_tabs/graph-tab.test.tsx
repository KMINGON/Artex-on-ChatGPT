import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "@/lib/api";
import type { ExplorationGraphPage, TaskNode } from "@/lib/types";

import messages from "../../../../../../../messages/ko.json";
import { GraphTab } from "./graph-tab";

vi.mock("@/lib/api", () => ({ api: { explorationGraph: vi.fn() } }));
vi.mock("@/components/exploration-graph", () => ({
  ExplorationGraph: ({ nodes }: { nodes: TaskNode[] }) => (
    <div data-testid="graph">{nodes.map((n) => `${n.id}:${n.state}`).join(",")}</div>
  ),
}));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  vi.useRealTimers();
});
const node = (id: string, state = "confirmed"): TaskNode => ({
  id,
  type: "fact",
  priority: 0,
  state,
  origin: "worker",
  ts: "",
});
const page = (ids: string[], more = false, next = ""): ExplorationGraphPage => ({
  nodes: ids.map((id) => node(id)),
  edges: [],
  total_nodes: 4,
  total_edges: 0,
  has_more: more,
  next_before: next,
});
function view(taskId = "1") {
  return (
    <NextIntlClientProvider locale="ko" messages={messages}>
      <GraphTab taskId={taskId} />
    </NextIntlClientProvider>
  );
}
it("expands connected pages without duplicating neighbours", async () => {
  vi.mocked(api.explorationGraph).mockImplementation(async (_task, query) =>
    query?.before ? page(["2", "1"]) : page(["4", "3", "1"], true, "3"),
  );
  render(view());
  expect(await screen.findByRole("button", { name: "이전 기록 더 불러오기" })).toBeEnabled();
  fireEvent.click(screen.getByRole("button", { name: "이전 기록 더 불러오기" }));
  await waitFor(() =>
    expect(screen.getByTestId("graph")).toHaveTextContent("4:confirmed,3:confirmed,1:confirmed,2:confirmed"),
  );
  expect(api.explorationGraph).toHaveBeenCalledWith("1", { before: "3", limit: 500 });
  expect(screen.queryByRole("button", { name: "이전 기록 더 불러오기" })).not.toBeInTheDocument();
});
it("shows a failed extension while preserving the last complete graph", async () => {
  vi.mocked(api.explorationGraph)
    .mockResolvedValueOnce(page(["4", "3"], true, "3"))
    .mockRejectedValue(new Error("offline"));
  render(view());
  fireEvent.click(await screen.findByRole("button", { name: "이전 기록 더 불러오기" }));
  expect(await screen.findByRole("alert")).toBeInTheDocument();
  expect(screen.getByTestId("graph")).toHaveTextContent("4:confirmed,3:confirmed");
});
it("does not let the previous task's late response replace the current task", async () => {
  let finish: (page: ExplorationGraphPage) => void = () => {
    /* Assigned by the pending request below. */
  };
  vi.mocked(api.explorationGraph).mockImplementation(async (taskId) =>
    taskId === "1"
      ? new Promise((resolve) => {
          finish = resolve;
        })
      : page(["9"]),
  );
  const { rerender } = render(view("1"));
  rerender(view("2"));
  await waitFor(() => expect(screen.getByTestId("graph")).toHaveTextContent("9:confirmed"));
  await act(async () => finish(page(["1"])));
  expect(screen.getByTestId("graph")).toHaveTextContent("9:confirmed");
  expect(screen.getByTestId("graph")).not.toHaveTextContent("1:confirmed");
});
it("polling replaces a deleted or hidden source node instead of retaining it", async () => {
  vi.useFakeTimers();
  vi.mocked(api.explorationGraph)
    .mockResolvedValueOnce(page(["4", "3"]))
    .mockResolvedValue({ ...page(["4"]), nodes: [node("4", "dismissed")] });
  render(view());
  await act(async () => {
    await Promise.resolve();
  });
  expect(screen.getByTestId("graph")).toHaveTextContent("3:confirmed");
  await act(async () => {
    await vi.advanceTimersByTimeAsync(20000);
  });
  expect(screen.getByTestId("graph")).toHaveTextContent("4:dismissed");
  expect(screen.getByTestId("graph")).not.toHaveTextContent("3:confirmed");
});
