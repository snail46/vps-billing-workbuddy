/**
 * Date and time formatting.
 *
 * docs/13 requires dates and times to be rendered for the viewer's locale and
 * time zone, and docs/04 stores everything as UTC. Formatting therefore always
 * receives an explicit time zone rather than relying on the runtime default,
 * which would make a timestamp render differently depending on which machine
 * produced the HTML.
 */
import type { SupportedLocale } from "../i18n/index.js";

/** Input accepted by the formatters. */
export type DateInput = string | number | Date;

/** Options shared by the formatters. */
export interface FormatDateTimeOptions {
  locale: SupportedLocale;
  /** IANA time zone, e.g. `Asia/Shanghai`. */
  timeZone?: string;
}

/**
 * Formats a date and time.
 *
 * An unparseable value renders as an em-free placeholder rather than "Invalid
 * Date", so a malformed timestamp degrades quietly instead of leaking a runtime
 * artefact into the interface.
 */
export function formatDateTime(value: DateInput, options: FormatDateTimeOptions): string {
  const date = toDate(value);
  if (date === null) {
    return "";
  }

  return new Intl.DateTimeFormat(options.locale, {
    dateStyle: "medium",
    timeStyle: "short",
    ...(options.timeZone === undefined ? {} : { timeZone: options.timeZone }),
  }).format(date);
}

/** Formats a date without a time component. */
export function formatDate(value: DateInput, options: FormatDateTimeOptions): string {
  const date = toDate(value);
  if (date === null) {
    return "";
  }

  return new Intl.DateTimeFormat(options.locale, {
    dateStyle: "medium",
    ...(options.timeZone === undefined ? {} : { timeZone: options.timeZone }),
  }).format(date);
}

/**
 * Formats a duration in seconds as the largest sensible unit.
 *
 * Used for uptime, where "2 days" is more legible than "172800 seconds".
 * Returns the count and the unit separately so the caller can translate the unit
 * and pluralise it through i18n rather than hardcoding either (docs/13).
 */
export function splitDuration(totalSeconds: number): {
  count: number;
  unit: "seconds" | "minutes" | "hours" | "days";
} {
  const seconds = Math.max(0, Math.floor(totalSeconds));

  if (seconds < 60) {
    return { count: seconds, unit: "seconds" };
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return { count: minutes, unit: "minutes" };
  }
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    return { count: hours, unit: "hours" };
  }
  return { count: Math.floor(hours / 24), unit: "days" };
}

function toDate(value: DateInput): Date | null {
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}
