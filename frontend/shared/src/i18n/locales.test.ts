import { describe, expect, it } from "vitest";

import { enUS } from "./locales/en-US.js";
import { zhCN } from "./locales/zh-CN.js";

/** Flattens a nested resource object into dotted key paths. */
function flattenKeys(value: unknown, prefix = ""): string[] {
  if (typeof value !== "object" || value === null) {
    return [prefix];
  }

  return Object.entries(value).flatMap(([key, child]) =>
    flattenKeys(child, prefix === "" ? key : `${prefix}.${key}`),
  );
}

/**
 * The backend emits i18n keys in an `errors.<code lowercased>` shape
 * (docs/13 and the httpx package). If a locale is missing one of them, a failure
 * renders as an empty message, which docs/19 classifies as a release blocker:
 * "critical error with no feedback".
 */
const BACKEND_ERROR_CODES = [
  "INTERNAL_ERROR",
  "VALIDATION_FAILED",
  "UNAUTHORIZED",
  "FORBIDDEN",
  "NOT_FOUND",
  "METHOD_NOT_ALLOWED",
  "CONFLICT",
  "RATE_LIMITED",
  "REQUEST_TIMEOUT",
  "SERVICE_UNAVAILABLE",
  "NETWORK_ERROR",
  "INVALID_RESPONSE",
] as const;

describe("i18n resource parity", () => {
  it("exposes the same key set in both locales", () => {
    const zhKeys = flattenKeys(zhCN).sort();
    const enKeys = flattenKeys(enUS).sort();

    // Comparing both directions reports the missing key by name, which a length
    // comparison would not.
    expect(zhKeys.filter((key) => !enKeys.includes(key)), "keys missing from en-US").toEqual([]);
    expect(enKeys.filter((key) => !zhKeys.includes(key)), "keys missing from zh-CN").toEqual([]);
  });

  it("defines a translation for every backend error code", () => {
    for (const code of BACKEND_ERROR_CODES) {
      const key = `errors.${code.toLowerCase()}`;
      expect(flattenKeys(zhCN), `zh-CN is missing ${key}`).toContain(key);
      expect(flattenKeys(enUS), `en-US is missing ${key}`).toContain(key);
    }
  });

  it("provides a generic fallback for unmapped failures", () => {
    // ErrorState falls back to this key, so it must exist or an unknown failure
    // would render as nothing at all.
    expect(flattenKeys(zhCN)).toContain("errors.unknown");
    expect(flattenKeys(enUS)).toContain("errors.unknown");
  });
});
