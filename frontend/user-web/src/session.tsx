/**
 * The session context.
 *
 * The session is a cookie the browser holds and a CSRF token the body holds.
 * The token lives here for the request duration; it is not persisted, so a
 * page reload recovers it from `GET /auth/me` — the same place the backend
 * documents it.
 */

import { ApiError, type ApiClient } from "@vps/shared";
import { useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { SessionContext, type SessionValue } from "./session-context.js";
import { login as loginRequest, logout as logoutRequest, whoami } from "./api/endpoints.js";
import type { UserSession } from "./api/types.js";

export function SessionProvider({ apiClient, children }: { apiClient: ApiClient; children: ReactNode }): ReactNode {
  const queryClient = useQueryClient();
  const [session, setSession] = useState<UserSession | null>(null);

  // The whoami query is the session's recovery path: one request on load, and
  // a 401 is simply "signed out", not an error screen.
  const { isPending } = useQuery({
    queryKey: ["session"],
    queryFn: async () => {
      try {
        const recovered = await whoami(apiClient);
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

  const value = useMemo<SessionValue>(
    () => ({
      session,
      resolving: isPending && session === null,
      signIn: async (email, password) => {
        const established = await loginRequest(apiClient, { email, password });
        setSession(established);
        await queryClient.invalidateQueries();
      },
      signOut: async () => {
        if (session !== null) {
          try {
            await logoutRequest(apiClient, session.csrf_token);
          } catch {
            // A failed logout still ends the client-side session; the cookie is
            // the credential, and the server's own expiry bounds its life.
          }
        }
        setSession(null);
        await queryClient.invalidateQueries();
      },
    }),
    [apiClient, isPending, queryClient, session],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

