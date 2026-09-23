import i18next, { type i18n as I18nInstance } from "i18next";
import { initReactI18next } from "react-i18next";

import { enUS } from "./locales/en-US.js";
import { zhCN } from "./locales/zh-CN.js";

/** Locales the platform supports, per docs/13-I18N-SPEC.md. */
export const SUPPORTED_LOCALES = ["zh-CN", "en-US"] as const;

/** A supported locale tag. */
export type SupportedLocale = (typeof SUPPORTED_LOCALES)[number];

/** Locale used when nothing else can be determined. */
export const DEFAULT_LOCALE: SupportedLocale = "zh-CN";

/** Where the chosen locale is remembered between visits. */
export const LOCALE_STORAGE_KEY = "vps.locale";

/** The single namespace both applications render from. */
export const DEFAULT_NAMESPACE = "translation";

/** i18n resources shared by both applications. */
export const resources = {
  "zh-CN": { [DEFAULT_NAMESPACE]: zhCN },
  "en-US": { [DEFAULT_NAMESPACE]: enUS },
} as const;

/** Narrows an arbitrary string to a supported locale. */
export function isSupportedLocale(value: string): value is SupportedLocale {
  return (SUPPORTED_LOCALES as readonly string[]).includes(value);
}

/**
 * Picks the initial locale.
 *
 * An explicit choice is remembered; otherwise the browser preference is used
 * when it matches a supported locale. The fallback is a deliberate default
 * rather than the browser's raw value, so an unsupported language does not
 * silently render the wrong locale's text.
 */
export function detectLocale(
  storage?: Pick<Storage, "getItem">,
  navigatorLanguage?: string,
): SupportedLocale {
  const stored = storage?.getItem(LOCALE_STORAGE_KEY);
  if (stored !== null && stored !== undefined && isSupportedLocale(stored)) {
    return stored;
  }

  if (navigatorLanguage !== undefined) {
    if (isSupportedLocale(navigatorLanguage)) {
      return navigatorLanguage;
    }
    // "zh-Hans-CN" and the like should resolve to a supported base tag rather
    // than falling all the way through to the default.
    const base = navigatorLanguage.split("-")[0];
    if (base === "zh") {
      return "zh-CN";
    }
    if (base === "en") {
      return "en-US";
    }
  }

  return DEFAULT_LOCALE;
}

/** Creates and initialises an i18next instance. */
export function createI18n(locale: SupportedLocale = DEFAULT_LOCALE): I18nInstance {
  const instance = i18next.createInstance();

  // void: init returns a promise, but every resource is bundled so the instance
  // is usable synchronously. React re-renders through the change event anyway.
  void instance.use(initReactI18next).init({
    resources,
    lng: locale,
    fallbackLng: DEFAULT_LOCALE,
    defaultNS: DEFAULT_NAMESPACE,
    supportedLngs: SUPPORTED_LOCALES,
    interpolation: {
      // React escapes rendered values already; double-escaping would corrupt
      // any text containing an ampersand or quote.
      escapeValue: false,
    },
    returnNull: false,
  });

  return instance;
}

/**
 * Reports the tone to use for a failure.
 *
 * Keeps the mapping from transport-level status to presentation in one place, so
 * every surface classifies a failure identically.
 */
export function failureTone(status: number): "warning" | "danger" {
  // A 4xx is the caller's or the data's problem and is recoverable by acting on
  // the message; a 5xx means the platform itself failed.
  return status >= 500 ? "danger" : "warning";
}
