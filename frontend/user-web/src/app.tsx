import type { ApiClient } from "@vps/shared";
import { LocaleSwitcher } from "@vps/shared";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { FoundationOverview } from "./foundation-overview.js";

/**
 * Application shell.
 *
 * Phase 0 deliberately ships a shell rather than product pages: TASKS.md scopes
 * the foundation to the application skeleton plus the shared layer, and the
 * catalog, checkout and instance screens belong to Phase 8.
 *
 * There is no router yet. Routing is introduced with the first real pages, in
 * the phase that owns them, rather than being wired against a single screen.
 */
export function App({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();

  return (
    <div className="min-h-full">
      <header className="border-b border-border-subtle bg-surface">
        <div className="mx-auto flex max-w-5xl items-center justify-between px-4 py-3">
          <span className="text-sm font-semibold text-content">{t("app.name")}</span>
          <LocaleSwitcher />
        </div>
      </header>

      <main className="mx-auto max-w-5xl space-y-4 px-4 py-6">
        <FoundationOverview apiClient={apiClient} />
      </main>
    </div>
  );
}
