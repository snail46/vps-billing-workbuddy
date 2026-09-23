import { LocaleSwitcher, type ApiClient } from "@vps/shared";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { PlatformStatus } from "./platform-status.js";

/**
 * Application shell.
 *
 * Phase 0 ships a shell rather than admin screens: TASKS.md scopes the
 * foundation to the application skeleton plus the shared layer, and the health
 * dashboard, user, product and ledger screens belong to Phase 9.
 *
 * The layout is denser than the user client's, which docs/11 permits and
 * expects for the operations surface.
 */
export function App({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();

  return (
    <div className="min-h-full">
      <header className="border-b border-border-subtle bg-surface">
        <div className="mx-auto flex max-w-7xl items-center justify-between gap-4 px-4 py-2">
          <span className="text-sm font-semibold text-content">{t("app.adminName")}</span>
          <LocaleSwitcher />
        </div>
      </header>

      <main className="mx-auto max-w-7xl px-4 py-4">
        <PlatformStatus apiClient={apiClient} />
      </main>
    </div>
  );
}
