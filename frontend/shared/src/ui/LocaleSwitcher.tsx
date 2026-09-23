import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import {
  DEFAULT_LOCALE,
  isSupportedLocale,
  LOCALE_STORAGE_KEY,
  SUPPORTED_LOCALES,
  type SupportedLocale,
} from "../i18n/index.js";

/**
 * Language selector shared by both applications.
 *
 * docs/13 requires both locales to be reachable from the interface. The choice
 * is persisted so a reload keeps the user's language rather than reverting to
 * the detected default.
 */
export function LocaleSwitcher(): ReactNode {
  const { t, i18n } = useTranslation();

  const current: SupportedLocale = isSupportedLocale(i18n.language)
    ? i18n.language
    : DEFAULT_LOCALE;

  const handleChange = (value: string): void => {
    if (!isSupportedLocale(value)) {
      return;
    }
    // changeLanguage re-renders every subscribed component, so no local state is
    // needed to keep the selector in sync.
    void i18n.changeLanguage(value);
    persistLocale(value);
  };

  return (
    <label className="flex items-center gap-2 text-xs text-content-muted">
      <span>{t("locale.label")}</span>
      <select
        value={current}
        onChange={(event) => {
          handleChange(event.target.value);
        }}
        className="rounded-md border border-border-subtle bg-surface px-2 py-1 text-xs text-content"
      >
        {SUPPORTED_LOCALES.map((locale) => (
          <option key={locale} value={locale}>
            {t(`locale.${locale}`)}
          </option>
        ))}
      </select>
    </label>
  );
}

/**
 * Writes the locale to local storage.
 *
 * Storage access is wrapped because it throws in some privacy configurations;
 * failing to remember a preference must never break the application.
 */
function persistLocale(locale: SupportedLocale): void {
  try {
    globalThis.localStorage?.setItem(LOCALE_STORAGE_KEY, locale);
  } catch {
    // Preference persistence is best-effort.
  }
}
