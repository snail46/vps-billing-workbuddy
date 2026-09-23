/**
 * Money formatting.
 *
 * Amounts cross the API as integer minor units (docs/04: `BIGINT` minor unit),
 * never as floating point. This module is the only place that converts them for
 * display, which is what keeps rounding decisions out of individual components.
 */
import type { SupportedLocale } from "../i18n/index.js";

/**
 * Currency exponent overrides.
 *
 * ISO 4217 defines the minor-unit exponent per currency. Almost every currency
 * uses 2, so the table records only the exceptions rather than an exhaustive
 * list; anything absent is treated as 2.
 */
const CURRENCY_EXPONENTS: Readonly<Record<string, number>> = {
  // Zero-decimal currencies.
  BIF: 0,
  CLP: 0,
  DJF: 0,
  GNF: 0,
  ISK: 0,
  JPY: 0,
  KMF: 0,
  KRW: 0,
  PYG: 0,
  RWF: 0,
  UGX: 0,
  UYI: 0,
  VND: 0,
  VUV: 0,
  XAF: 0,
  XOF: 0,
  XPF: 0,
  // Three-decimal currencies.
  BHD: 3,
  IQD: 3,
  JOD: 3,
  KWD: 3,
  LYD: 3,
  OMR: 3,
  TND: 3,
};

/** Default minor-unit exponent. */
const DEFAULT_EXPONENT = 2;

/** Returns the ISO 4217 minor-unit exponent for a currency. */
export function currencyExponent(currency: string): number {
  return CURRENCY_EXPONENTS[currency.toUpperCase()] ?? DEFAULT_EXPONENT;
}

/** Options for {@link formatMoney}. */
export interface FormatMoneyOptions {
  /** Locale used for grouping and symbol placement. */
  locale: SupportedLocale;
  /** ISO 4217 currency code, e.g. `CNY`. */
  currency: string;
  /**
   * Render the currency code instead of its symbol.
   *
   * Useful where several currencies appear together and symbols would be
   * ambiguous.
   */
  useCode?: boolean;
}

/**
 * Formats an integer minor-unit amount.
 *
 * The exponent is applied with big-integer arithmetic and the fractional part is
 * rebuilt as a decimal string, so the value handed to the formatter is the double
 * nearest to the exact amount rather than the result of dividing a value that was
 * already rounded.
 *
 * The return type is `number` because that is what `Intl.NumberFormat` accepts.
 * Amounts beyond 2^53 major units would therefore still be subject to double
 * rounding — a deliberate limit, since no currency amount approaches that
 * magnitude and a `number`-based formatter could not represent one anyway.
 */
export function formatMoney(minorUnits: number | bigint, options: FormatMoneyOptions): string {
  const currency = options.currency.toUpperCase();
  const exponent = currencyExponent(currency);
  const major = scaleMinorUnits(minorUnits, exponent);

  return new Intl.NumberFormat(options.locale, {
    style: "currency",
    currency,
    currencyDisplay: options.useCode === true ? "code" : "symbol",
    minimumFractionDigits: exponent,
    maximumFractionDigits: exponent,
  }).format(major);
}

/**
 * Converts minor units to a major-unit number.
 *
 * Exported for tests and for callers that must feed a numeric value into a
 * formatter with different rounding rules.
 */
export function scaleMinorUnits(minorUnits: number | bigint, exponent: number): number {
  const value = typeof minorUnits === "bigint" ? minorUnits : BigInt(minorUnits);
  const divisor = 10n ** BigInt(exponent);
  const whole = value / divisor;
  const remainder = value % divisor;

  if (remainder === 0n) {
    return Number(whole);
  }

  // Reconstruct the fractional part as a decimal string so that binary floating
  // point never sees the intermediate value.
  const sign = value < 0n ? "-" : "";
  const fraction = (remainder < 0n ? -remainder : remainder)
    .toString()
    .padStart(exponent, "0");

  return Number(`${sign}${whole < 0n ? -whole : whole}.${fraction}`);
}
