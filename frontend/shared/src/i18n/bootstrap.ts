import type { i18n as I18nInstance } from "i18next";

import { createI18n, detectLocale, type SupportedLocale } from "./index.js";

/**
 * Creates the i18n instance an application should use.
 *
 * Both applications need identical behaviour here — detect a locale, build the
 * instance, and keep the document title translated — so it lives in the shared
 * package rather than being re-implemented per app, where the two copies would
 * eventually disagree.
 *
 * `titleKey` lets the admin client use its own title while sharing everything
 * else.
 */
export function bootstrapI18n(titleKey = "app.name"): I18nInstance {
  const instance = createI18n(detectLocale(readStoredLocale, browserLanguage()));

  applyDocumentTitle(instance, titleKey);
  // Without this, switching language would update the interface but leave the
  // browser tab showing the previous language.
  instance.on("languageChanged", () => {
    applyDocumentTitle(instance, titleKey);
  });

  return instance;
}

function applyDocumentTitle(instance: I18nInstance, titleKey: string): void {
  // The title is user-visible text, so it comes from the resources rather than
  // from index.html (docs/13).
  globalThis.document.title = instance.t(titleKey);
}

/** Minimal storage reader that tolerates storage being unavailable. */
export const readStoredLocale: Pick<Storage, "getItem"> = {
  getItem(key: string): string | null {
    try {
      return globalThis.localStorage?.getItem(key) ?? null;
    } catch {
      // Storage can throw in some privacy configurations.
      return null;
    }
  },
};

function browserLanguage(): string | undefined {
  const language = globalThis.navigator?.language;
  return language === undefined || language === "" ? undefined : language;
}

/** Re-exported for callers that need the resolved type. */
export type { SupportedLocale };
