/**
 * The console shell: dense navigation, the admin session actions, the
 * sign-in screen. docs/11 permits higher density here; the hierarchy still
 * has to be legible.
 */

import { Button, Card, LocaleSwitcher } from "@vps/shared";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";
import { Link, NavLink, Outlet, useNavigate } from "react-router-dom";

import { useAdminSession } from "./session-context.js";

function navClass(active: boolean): string {
  return active
    ? "rounded bg-surface-muted px-2 py-1 text-xs font-medium text-content"
    : "rounded px-2 py-1 text-xs text-content-muted hover:bg-surface-muted hover:text-content";
}

const NAV = [
  ["/", "admin.nav.dashboard"],
  ["/users", "admin.nav.users"],
  ["/products", "admin.nav.products"],
  ["/orders", "admin.nav.orders"],
  ["/payments", "admin.nav.payments"],
  ["/ledger", "admin.nav.ledger"],
  ["/subscriptions", "admin.nav.subscriptions"],
  ["/instances", "admin.nav.instances"],
  ["/operations", "admin.nav.operations"],
  ["/tickets", "admin.nav.tickets"],
  ["/audit", "admin.nav.audit"],
  ["/admins", "admin.nav.admins"],
  ["/roles", "admin.nav.roles"],
  ["/settings", "admin.nav.settings"],
] as const;

export function Layout(): ReactNode {
  const { t } = useTranslation();
  const { session, signOut } = useAdminSession();
  const navigate = useNavigate();

  if (session === null) {
    return <Outlet />;
  }

  return (
    <div className="min-h-full">
      <header className="border-b border-border-subtle bg-surface">
        <div className="mx-auto max-w-7xl px-4 py-2">
          <div className="flex items-center justify-between gap-4">
            <span className="text-sm font-semibold text-content">{t("app.adminName")}</span>
            <div className="flex items-center gap-2">
              <LocaleSwitcher />
              <button
                type="button"
                className="rounded border border-border-subtle px-2 py-1 text-xs text-content-muted hover:bg-surface-muted hover:text-content"
                onClick={() => {
                  void signOut().then(() => navigate("/login"));
                }}
              >
                {t("admin.nav.signOut")}
              </button>
            </div>
          </div>
          <nav className="mt-2 flex flex-wrap items-center gap-1">
            {NAV.map(([to, key]) => (
              <NavLink key={to} to={to} className={({ isActive }) => navClass(isActive)} end={to === "/"}>
                {t(key)}
              </NavLink>
            ))}
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-7xl space-y-3 px-4 py-4">
        <Outlet />
      </main>
    </div>
  );
}

export function LoginScreen(): ReactNode {
  const { t } = useTranslation();
  const { signIn } = useAdminSession();
  const navigate = useNavigate();

  return (
    <div className="mx-auto max-w-sm pt-16">
      <Card title={t("admin.auth.signInTitle")}>
        <form
          className="space-y-2"
          onSubmit={(event) => {
            event.preventDefault();
            const email = event.currentTarget.elements.namedItem("email");
            const password = event.currentTarget.elements.namedItem("password");
            if (email instanceof HTMLInputElement && password instanceof HTMLInputElement) {
              void signIn(email.value, password.value).then(() => navigate("/"));
            }
          }}
        >
          <input name="email" type="email" required placeholder={t("admin.auth.email")} className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content" />
          <input name="password" type="password" required placeholder={t("admin.auth.password")} className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content" />
          <Button type="submit">{t("admin.auth.submit")}</Button>
          <p className="text-xs text-content-muted">
            <Link to="/">{t("admin.nav.dashboard")}</Link>
          </p>
        </form>
      </Card>
    </div>
  );
}
