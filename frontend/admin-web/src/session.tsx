/**
 * The admin session provider.
 *
 * The admin session is a separate credential space (ADR-004): its own cookie,
 * its own storage prefix, its own sign-in. The whoami on load is how a
 * refreshed console recovers the session and the CSRF token beside it.
 */

import { ApiError, type ApiClient } from "@vps/shared";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";

import type { AdminSession } from "./api.js";
import { AdminSessionContext, type AdminSessionValue } from "./session-context.js";

export function AdminSessionProvider({ apiClient, children }: { apiClient: ApiClient; children: ReactNode }): ReactNode {
  const queryClient = useQueryClient();
  const [session, setSession] = useState<AdminSession | null>(null);

  const { isPending } = useQuery({
    queryKey: ["admin-session"],
    queryFn: async () => {
      try {
        const recovered = await apiClient.request<AdminSession>("/api/v1/admin/auth/me");
        setSession(recovered);
        return recovered;
      } catch (cause) {
        if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) {
          setSession(null);
          return null;
        }
        throw cause;
      }
    },
    staleTime: 60_000,
    retry: false,
  });

  const value = useMemo<AdminSessionValue>(
    () => ({
      session,
      resolving: isPending && session === null,
      signIn: async (email, password) => {
        const established = await apiClient.request<AdminSession>("/api/v1/admin/auth/login", {
          method: "POST",
          body: { email, password },
        });
        setSession(established);
        await queryClient.invalidateQueries();
      },
      signOut: async () => {
        if (session !== null) {
          try {
            await apiClient.request("/api/v1/admin/auth/logout", {
              method: "POST",
              headers: { "X-CSRF-Token": session.csrf_token },
            });
          } catch {
            // The cookie is the credential; the client-side session ends either way.
          }
        }
        setSession(null);
        await queryClient.invalidateQueries();
      },
    }),
    [apiClient, isPending, queryClient, session],
  );

  return <AdminSessionContext.Provider value={value}>{children}</AdminSessionContext.Provider>;
}
