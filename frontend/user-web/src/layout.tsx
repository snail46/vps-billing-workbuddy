/**
 * The application shell: navigation, locale switcher, session state.
 *
 * docs/10's pages hang off this header; the unread badge and the session
 * actions read from the session context, not from per-page state.
 */

import { LocaleSwitcher } from "@vps/shared";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";

import { useSession } from "./session-context.js";

function navClass(active: boolean): string {
  return active
    ? "rounded-md bg-surface-muted px-3 py-1.5 text-sm font-medium text-content"
    : "rounded-md px-3 py-1.5 text-sm text-content-muted hover:bg-surface-muted hover:text-content";
}

export function Layout(): ReactNode {
  const { t } = useTranslation();
  const { session, signOut } = useSession();
  const navigate = useNavigate();

  const items: Array<{ to: string; key: string }> = session
    ? [
        { to: "/", key: "user.nav.dashboard" },
        { to: "/catalog", key: "user.nav.catalog" },
        { to: "/instances", key: "user.nav.instances" },
        { to: "/orders", key: "user.nav.orders" },
        { to: "/invoices", key: "user.nav.invoices" },
        { to: "/wallet", key: "user.nav.wallet" },
        { to: "/notifications", key: "user.nav.notifications" },
        { to: "/tickets", key: "user.nav.tickets" },
        { to: "/account", key: "user.nav.account" },
      ]
    : [{ to: "/catalog", key: "user.nav.catalog" }];

  return (
    <div className="min-h-full">
      <header className="border-b border-border-subtle bg-surface">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-2 px-4 py-3">
          <NavLink to="/" className="mr-2 text-sm font-semibold text-content">
            {t("app.name")}
          </NavLink>
          {session !== null && (
            <nav className="flex flex-wrap items-center gap-1">
              {items.map((item) => (
                <NavLink key={item.to} to={item.to} className={({ isActive }) => navClass(isActive)} end={item.to === "/"}>
                  {t(item.key)}
                </NavLink>
              ))}
            </nav>
          )}
          <div className="ml-auto flex items-center gap-2">
            <LocaleSwitcher />
            {session === null ? (
              <button
                type="button"
                className="rounded-md border border-border-subtle px-3 py-1.5 text-sm text-content hover:bg-surface-muted"
                onClick={() => void navigate("/login")}
              >
                {t("user.nav.signIn")}
              </button>
            ) : (
              <button
                type="button"
                className="rounded-md border border-border-subtle px-3 py-1.5 text-sm text-content-muted hover:bg-surface-muted hover:text-content"
                onClick={() => {
                  void signOut().then(() => navigate("/"));
                }}
              >
                {t("user.nav.signOut")}
              </button>
            )}
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-6xl space-y-4 px-4 py-6">
        <Outlet />
      </main>
    </div>
  );
}
