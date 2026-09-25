/**
 * The sign-in and registration screens.
 *
 * Registration deliberately does not sign in: it reports success and hands
 * the visitor to the sign-in form, which is the one place credentials are
 * issued.
 */

import { Button, Card, type ApiClient } from "@vps/shared";
import { useTranslation } from "react-i18next";
import { useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";

import { register } from "../api/endpoints.js";
import { useSession } from "../session-context.js";

export function LoginPage(): ReactNode {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { signIn } = useSession();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [failure, setFailure] = useState<string | null>(null);
  const [registered] = useState(() => new URLSearchParams(window.location.search).has("registered"));

  return (
    <Page title={t("user.auth.signInTitle")}>
      <Card>
        <form
          className="space-y-2"
          onSubmit={(event) => {
            event.preventDefault();
            signIn(email, password)
              .then(() => navigate("/"))
              .catch((cause: { messageKey?: string }) => setFailure(cause.messageKey ?? null));
          }}
        >
          {registered && <p className="text-sm text-status-success-content">{t("user.auth.registered")}</p>}
          <input
            type="email"
            required
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            placeholder={t("user.auth.email")}
            className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content"
          />
          <input
            type="password"
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            placeholder={t("user.auth.password")}
            className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content"
          />
          <Button type="submit">{t("user.auth.submitSignIn")}</Button>
          {failure !== null && <p className="text-sm text-status-danger-content">{t(failure)}</p>}
          <p className="text-xs text-content-muted">
            {t("user.auth.needAccount")}{" "}
            <Link to="/register" className="underline text-content">
              {t("user.nav.register")}
            </Link>
          </p>
        </form>
      </Card>
    </Page>
  );
}

function Page({ title, children }: { title: string; children: ReactNode }): ReactNode {
  return (
    <div className="mx-auto max-w-md space-y-4 pt-8">
      <h1 className="text-lg font-semibold text-content">{title}</h1>
      {children}
    </div>
  );
}

export function RegisterPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [failure, setFailure] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  return (
    <Page title={t("user.auth.registerTitle")}>
      <Card>
        {done ? (
          <p className="text-sm text-status-success-content">{t("user.auth.registered")}</p>
        ) : (
          <form
            className="space-y-2"
            onSubmit={(event) => {
              event.preventDefault();
              register(apiClient, { email, password })
                .then(() => setDone(true))
                .catch((cause: { messageKey?: string }) => setFailure(cause.messageKey ?? null));
            }}
          >
            <input
              type="email"
              required
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              placeholder={t("user.auth.email")}
              className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content"
            />
            <input
              type="password"
              required
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              placeholder={t("user.auth.password")}
              className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content"
            />
            <Button type="submit">{t("user.auth.submitRegister")}</Button>
            {failure !== null && <p className="text-sm text-status-danger-content">{t(failure)}</p>}
            <p className="text-xs text-content-muted">
              {t("user.auth.haveAccount")}{" "}
              <Link to="/login" className="underline text-content">
                {t("user.nav.signIn")}
              </Link>
            </p>
          </form>
        )}
      </Card>
    </Page>
  );
}
