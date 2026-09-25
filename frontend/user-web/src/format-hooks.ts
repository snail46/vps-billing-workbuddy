/**
 * Locale-aware formatting hooks.
 *
 * They are hooks rather than free functions because the active locale is
 * React state; they live apart from the page fragments so that a file
 * exporting components exports only components (react-refresh).
 */

import { formatDateTime, formatMoney, isSupportedLocale, type SupportedLocale } from "@vps/shared";
import { useTranslation } from "react-i18next";

function asLocale(language: string): SupportedLocale {
  return isSupportedLocale(language) ? language : "zh-CN";
}

export function useMoney(): (minor: number, currency: string) => string {
  const { i18n } = useTranslation();
  const locale = asLocale(i18n.language);
  return (minor, currency) => formatMoney(minor, { locale, currency });
}

export function useDate(): (value: string | null | undefined) => string {
  const { i18n } = useTranslation();
  const locale = asLocale(i18n.language);
  return (value) => (value ? formatDateTime(value, { locale }) : "—");
}

/** An i18n-keyed object (e.g. a plan's name) rendered in the active locale. */
export function useLocalizedText(): (value: Record<string, string> | null | undefined, fallback: string) => string {
  const { i18n } = useTranslation();
  return (value, fallback) => {
    if (value === null || value === undefined) {
      return fallback;
    }
    return value[i18n.language] ?? value["en-US"] ?? Object.values(value)[0] ?? fallback;
  };
}
