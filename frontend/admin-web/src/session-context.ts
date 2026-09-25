/**
 * The admin session context and hook, apart from the provider so the provider
 * file exports only components (react-refresh).
 */

import { createContext, useContext } from "react";

import type { AdminSession } from "./api.js";

export interface AdminSessionValue {
  session: AdminSession | null;
  resolving: boolean;
  signIn: (email: string, password: string) => Promise<void>;
  signOut: () => Promise<void>;
}

export const AdminSessionContext = createContext<AdminSessionValue | null>(null);

export function useAdminSession(): AdminSessionValue {
  const value = useContext(AdminSessionContext);
  if (value === null) {
    throw new Error("useAdminSession must be used inside AdminSessionProvider");
  }
  return value;
}
