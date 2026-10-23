import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { LOCALE_STORAGE_KEY } from "./config";
import { getDateLocale } from "./datetime";
import { setActiveLocale, useActiveLocale } from "./locale-store";

function LocaleView() {
  return <p>{useActiveLocale()}</p>;
}

afterEach(() => {
  cleanup();
  localStorage.clear();
});

describe("saved UI language", () => {
  it.each(["zh", "ja", "es"])("normalizes retired %s preferences to Korean", (locale) => {
    localStorage.setItem(LOCALE_STORAGE_KEY, locale);
    render(<LocaleView />);
    expect(screen.getByText("ko")).toBeInTheDocument();
    expect(getDateLocale()).toBe("ko-KR");
  });

  it("retains English and updates the UI, date formatting and saved preference together", () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, "en");
    render(<LocaleView />);
    expect(screen.getByText("en")).toBeInTheDocument();
    expect(getDateLocale()).toBe("en-US");
    act(() => setActiveLocale("ko"));
    expect(screen.getByText("ko")).toBeInTheDocument();
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("ko");
    expect(getDateLocale()).toBe("ko-KR");
  });
});
