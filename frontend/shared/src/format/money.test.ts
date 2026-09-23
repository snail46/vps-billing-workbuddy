import { describe, expect, it } from "vitest";

import { currencyExponent, formatMoney, scaleMinorUnits } from "./money.js";

describe("currencyExponent", () => {
  it("defaults to two decimal places", () => {
    expect(currencyExponent("USD")).toBe(2);
    expect(currencyExponent("CNY")).toBe(2);
    expect(currencyExponent("EUR")).toBe(2);
  });

  it("knows the zero-decimal currencies", () => {
    expect(currencyExponent("JPY")).toBe(0);
    expect(currencyExponent("KRW")).toBe(0);
    expect(currencyExponent("VND")).toBe(0);
  });

  it("knows the three-decimal currencies", () => {
    expect(currencyExponent("KWD")).toBe(3);
    expect(currencyExponent("BHD")).toBe(3);
  });

  it("is case insensitive", () => {
    expect(currencyExponent("jpy")).toBe(0);
  });
});

describe("scaleMinorUnits", () => {
  it("converts minor units to major units", () => {
    expect(scaleMinorUnits(1234, 2)).toBe(12.34);
    expect(scaleMinorUnits(100, 2)).toBe(1);
    expect(scaleMinorUnits(1234, 0)).toBe(1234);
    expect(scaleMinorUnits(1234, 3)).toBe(1.234);
  });

  it("handles negative amounts", () => {
    // Refunds and ledger adjustments are negative; dropping the sign here would
    // silently misreport a correction.
    expect(scaleMinorUnits(-1234, 2)).toBe(-12.34);
    expect(scaleMinorUnits(-100, 2)).toBe(-1);
    expect(scaleMinorUnits(-1, 2)).toBe(-0.01);
  });

  it("pads the fractional part to the currency exponent", () => {
    // 5 minor units is 0.05, not 0.5: the remainder must be left-padded.
    expect(scaleMinorUnits(5, 2)).toBe(0.05);
    expect(scaleMinorUnits(50, 2)).toBe(0.5);
    expect(scaleMinorUnits(5, 3)).toBe(0.005);
  });

  it("accepts minor units beyond the safe integer range", () => {
    // The input is passed as a bigint because it is not representable as a
    // double. Rebuilding the fraction from the remainder is what keeps the
    // trailing cents from vanishing.
    expect(scaleMinorUnits(9_007_199_254_740_995n, 2)).toBe(
      Number("90071992547409.95"),
    );
  });

  it("does not lose the trailing zero of a fraction", () => {
    // 10 minor units is 0.10, not 0.1 formatted as 0.1 — the exponent pads it.
    expect(scaleMinorUnits(10, 2)).toBe(0.1);
  });
});

describe("formatMoney", () => {
  it("renders the currency and the amount", () => {
    const formatted = formatMoney(123456, { locale: "en-US", currency: "USD" });
    expect(formatted).toContain("1,234.56");
    expect(formatted).toContain("$");
  });

  it("respects the currency exponent", () => {
    // 1234 minor units of JPY is 1234 yen, not 12.34.
    const formatted = formatMoney(1234, { locale: "en-US", currency: "JPY" });
    expect(formatted).toContain("1,234");
    expect(formatted).not.toContain(".");
  });

  it("can render the currency code instead of the symbol", () => {
    const formatted = formatMoney(100, { locale: "en-US", currency: "CNY", useCode: true });
    expect(formatted).toContain("CNY");
  });

  it("accepts a bigint, as the API may deliver one", () => {
    const formatted = formatMoney(123456n, { locale: "en-US", currency: "USD" });
    expect(formatted).toContain("1,234.56");
  });
});
