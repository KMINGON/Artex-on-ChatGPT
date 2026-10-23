import { describe, expect, it } from "vitest";

import { DEFAULT_LOCALE, isLocale, LOCALE_LABELS, LOCALES, resolveLocale } from "./config";

describe("i18n config", () => {
  it("exposes the supported locales with native labels", () => {
    expect([...LOCALES]).toEqual(["en", "ko"]);
    expect(Object.keys(LOCALE_LABELS).sort()).toEqual([...LOCALES].sort());
    expect(LOCALE_LABELS.ko).toBe("한국어");
    expect(LOCALE_LABELS.en).toBe("English");
  });

  it("isLocale narrows known locale strings only", () => {
    expect(isLocale("ko")).toBe(true);
    expect(isLocale("es")).toBe(false);
    expect(isLocale("ja")).toBe(false);
    expect(isLocale("zh")).toBe(false);
    expect(isLocale("fr")).toBe(false);
    expect(isLocale("")).toBe(false);
    expect(isLocale(null)).toBe(false);
    expect(isLocale(undefined)).toBe(false);
    expect(isLocale(1)).toBe(false);
  });

  it("resolveLocale falls back to the default unless NEXT_PUBLIC_LOCALE is supported", () => {
    const original = process.env.NEXT_PUBLIC_LOCALE;
    try {
      process.env.NEXT_PUBLIC_LOCALE = "en";
      expect(resolveLocale()).toBe("en");
      process.env.NEXT_PUBLIC_LOCALE = "zh";
      expect(resolveLocale()).toBe("ko");

      process.env.NEXT_PUBLIC_LOCALE = "fr";
      expect(resolveLocale()).toBe(DEFAULT_LOCALE);

      delete process.env.NEXT_PUBLIC_LOCALE;
      expect(resolveLocale()).toBe(DEFAULT_LOCALE);
    } finally {
      if (original === undefined) delete process.env.NEXT_PUBLIC_LOCALE;
      else process.env.NEXT_PUBLIC_LOCALE = original;
    }
  });
});
