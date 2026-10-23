import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "@/lib/api";

import messages from "../../messages/ko.json";
import { TestPresetPicker } from "./test-preset-picker";

vi.mock("@/lib/api", () => ({ api: { presets: vi.fn() } }));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
it("keeps custom by default and sends the selected policy id", async () => {
  vi.mocked(api.presets).mockResolvedValue({
    resources: [],
    tools: [],
    policies: [
      {
        id: "standard",
        version: 1,
        name: "운영 · 일반 검증",
        name_en: "Production · standard",
        description: "임시 자료 변경 허용",
        description_en: "Temporary changes allowed",
        constraints: [],
      },
    ],
  });
  const onChange = vi.fn();
  render(
    <NextIntlClientProvider locale="ko" messages={messages}>
      <TestPresetPicker value="" onChange={onChange} />
    </NextIntlClientProvider>,
  );
  await screen.findByRole("option", { name: "운영 · 일반 검증" });
  expect(screen.getByRole("combobox")).toHaveValue("");
  expect(onChange).not.toHaveBeenCalled();
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "standard" } });
  expect(onChange).toHaveBeenCalledWith("standard");
});
