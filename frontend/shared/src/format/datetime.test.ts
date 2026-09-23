import { describe, expect, it } from "vitest";

import { formatDate, formatDateTime, splitDuration } from "./datetime.js";

describe("formatDateTime", () => {
  const instant = "2026-09-23T06:30:00Z";

  it("formats for the requested locale and time zone", () => {
    const utc = formatDateTime(instant, { locale: "en-US", timeZone: "UTC" });
    const shanghai = formatDateTime(instant, { locale: "en-US", timeZone: "Asia/Shanghai" });

    // docs/13 requires locale- and zone-aware rendering; the same instant must
    // not render identically in two zones.
    expect(utc).not.toBe(shanghai);
    expect(shanghai).toMatch(/2:30/);
  });

  it("accepts Date, string and epoch input", () => {
    const fromDate = formatDateTime(new Date(instant), { locale: "en-US", timeZone: "UTC" });
    const fromEpoch = formatDateTime(Date.parse(instant), { locale: "en-US", timeZone: "UTC" });

    expect(fromDate).toBe(fromEpoch);
  });

  it("returns an empty string for an unparseable value", () => {
    // "Invalid Date" must never reach the interface.
    expect(formatDateTime("not-a-date", { locale: "en-US" })).toBe("");
  });
});

describe("formatDate", () => {
  it("omits the time component", () => {
    const formatted = formatDate("2026-09-23T06:30:00Z", { locale: "en-US", timeZone: "UTC" });

    expect(formatted).toContain("2026");
    expect(formatted).not.toMatch(/\d{1,2}:\d{2}/);
  });

  it("returns an empty string for an unparseable value", () => {
    expect(formatDate("nonsense", { locale: "en-US" })).toBe("");
  });
});

describe("splitDuration", () => {
  it("picks the largest sensible unit", () => {
    // The unit is returned separately so the caller translates it rather than
    // this module hardcoding English (docs/13).
    expect(splitDuration(45)).toEqual({ count: 45, unit: "seconds" });
    expect(splitDuration(60)).toEqual({ count: 1, unit: "minutes" });
    expect(splitDuration(3599)).toEqual({ count: 59, unit: "minutes" });
    expect(splitDuration(3600)).toEqual({ count: 1, unit: "hours" });
    expect(splitDuration(86_399)).toEqual({ count: 23, unit: "hours" });
    expect(splitDuration(86_400)).toEqual({ count: 1, unit: "days" });
  });

  it("clamps negatives to zero", () => {
    expect(splitDuration(-5)).toEqual({ count: 0, unit: "seconds" });
  });
});
