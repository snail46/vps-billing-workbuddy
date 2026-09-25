/**
 * The admin endpoint layer.
 *
 * The admin payloads are rows rendered for the screens; the pages read them
 * through small typed views. The CSRF token comes from the admin session
 * body, exactly as the user client receives its own.
 */

export interface AdminSession {
  admin: {
    id: string;
    email: string;
    display_name: string;
    status: string;
    two_factor_enabled: boolean;
    permissions: string[];
  };
  csrf_token: string;
  expires_at: string;
}

export const paths = {
  login: "/api/v1/admin/auth/login",
  logout: "/api/v1/admin/auth/logout",
  me: "/api/v1/admin/auth/me",
  overview: "/api/v1/admin/overview",
  users: "/api/v1/admin/users",
  user: (id: string) => `/api/v1/admin/users/${id}`,
  suspendUser: (id: string) => `/api/v1/admin/users/${id}/suspend`,
  activateUser: (id: string) => `/api/v1/admin/users/${id}/activate`,
  products: "/api/v1/admin/products",
  orders: "/api/v1/admin/orders",
  order: (id: string) => `/api/v1/admin/orders/${id}`,
  payments: "/api/v1/admin/payments",
  ledger: "/api/v1/admin/ledger",
  subscriptions: "/api/v1/admin/subscriptions",
  instances: "/api/v1/admin/instances",
  instance: (id: string) => `/api/v1/admin/instances/${id}`,
  operations: "/api/v1/admin/operations",
  operation: (id: string) => `/api/v1/admin/operations/${id}`,
  tickets: "/api/v1/admin/tickets",
  ticket: (id: string) => `/api/v1/admin/tickets/${id}`,
  ticketMessages: (id: string) => `/api/v1/admin/tickets/${id}/messages`,
  ticketClose: (id: string) => `/api/v1/admin/tickets/${id}/close`,
  audit: "/api/v1/admin/audit",
  admins: "/api/v1/admin/admins",
  roles: "/api/v1/admin/roles",
  settings: "/api/v1/admin/settings",
} as const;

/** Reads a row's string field the pages may or may not see. */
export function str(row: Record<string, unknown>, key: string): string {
  const value = row[key];
  return typeof value === "string" ? value : value !== null && value !== undefined ? String(value) : "—";
}

export function num(row: Record<string, unknown>, key: string): number {
  const value = row[key];
  return typeof value === "number" ? value : 0;
}
