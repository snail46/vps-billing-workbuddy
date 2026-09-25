/**
 * The session's shared context and its hook.
 *
 * They live apart from the provider so that a file exporting a component
 * exports only components (react-refresh), and so that the hook cannot
 * accidentally become the place where session state is written.
 */

import { createContext, useContext } from "react";

import type { UserSession } from "./api/types.js";

export interface SessionValue {
  /** The session, while it is being recovered or once recovered. */
  session: UserSession | null;
  /** True until the first whoami answers. */
  resolving: boolean;
  signIn: (email: string, password: string) => Promise<void>;
  signOut: () => Promise<void>;
}

export const SessionContext = createContext<SessionValue | null>(null);

export function useSession(): SessionValue {
  const value = useContext(SessionContext);
  if (value === null) {
    throw new Error("useSession must be used inside SessionProvider");
  }
  return value;
}
