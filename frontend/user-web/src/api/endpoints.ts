/**
 * The user web's endpoint layer.
 *
 * One function per endpoint, so a route rename is a compile error here rather
 * than a broken screen. The CSRF token is injected from the session store by
 * the caller — it is issued with the session body and never readable from the
 * HttpOnly cookie.
 */

import type {
  InstanceDetail,
  InvoiceDetail,
  NotificationList,
  OperationDetail,
  Order,
  PaymentIntent,
  Product,
  Subscription,
  TicketDetail,
  UserSession,
  WalletView,
} from "./types.js";

/** Paths of the product API, relative to the base URL. */
export const paths = {
  register: "/api/v1/auth/register",
  login: "/api/v1/auth/login",
  logout: "/api/v1/auth/logout",
  me: "/api/v1/auth/me",
  products: "/api/v1/products",
  orders: "/api/v1/orders",
  order: (id: string) => `/api/v1/orders/${id}`,
  startPayment: (orderID: string) => `/api/v1/orders/${orderID}/payments`,
  subscriptions: "/api/v1/subscriptions",
  cancelSubscription: (id: string) => `/api/v1/subscriptions/${id}/cancel`,
  renewSubscription: (id: string) => `/api/v1/subscriptions/${id}/renew`,
  instances: "/api/v1/instances",
  instance: (id: string) => `/api/v1/instances/${id}`,
  restartInstance: (id: string) => `/api/v1/instances/${id}/restart`,
  reinstallInstance: (id: string) => `/api/v1/instances/${id}/reinstall`,
  operation: (id: string) => `/api/v1/operations/${id}`,
  events: "/api/v1/events",
  wallet: "/api/v1/wallet",
  invoices: "/api/v1/invoices",
  invoice: (id: string) => `/api/v1/invoices/${id}`,
  notifications: "/api/v1/notifications",
  markNotificationRead: (id: string) => `/api/v1/notifications/${id}/read`,
  tickets: "/api/v1/tickets",
  ticket: (id: string) => `/api/v1/tickets/${id}`,
  ticketMessages: (id: string) => `/api/v1/tickets/${id}/messages`,
  ticketClose: (id: string) => `/api/v1/tickets/${id}/close`,
} as const;

export interface RegisterInput {
  email: string;
  password: string;
  locale?: string;
}

export interface LoginInput {
  email: string;
  password: string;
}

/** The catalogue: active products with their active plans, cheapest first. */
export async function listProducts(client: { request: <T>(path: string, options?: object) => Promise<T> }): Promise<Product[]> {
  const data = await client.request<{ products: Product[] }>(paths.products);
  return data.products;
}

export async function whoami(client: { request: <T>(path: string, options?: object) => Promise<T> }): Promise<UserSession> {
  return client.request<UserSession>(paths.me);
}

export async function login(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  input: LoginInput,
): Promise<UserSession> {
  return client.request<UserSession>(paths.login, { method: "POST", body: input });
}

export async function register(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  input: RegisterInput,
): Promise<void> {
  await client.request(paths.register, { method: "POST", body: input });
}

export async function logout(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  csrfToken: string,
): Promise<void> {
  await client.request(paths.logout, { method: "POST", headers: csrfHeader(csrfToken) });
}

export function csrfHeader(token: string): Record<string, string> {
  return { "X-CSRF-Token": token };
}

export async function listOrders(client: { request: <T>(path: string, options?: object) => Promise<T> }): Promise<Order[]> {
  const data = await client.request<{ orders: Order[] }>(paths.orders);
  return data.orders;
}

export async function getOrder(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  id: string,
): Promise<Order> {
  return client.request<Order>(paths.order(id));
}

export async function startPayment(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  csrfToken: string,
  orderID: string,
  gateway: string,
): Promise<PaymentIntent> {
  const data = await client.request<{ payment: PaymentIntent }>(paths.startPayment(orderID), {
    method: "POST",
    body: { gateway },
    headers: csrfHeader(csrfToken),
  });
  return data.payment;
}

export async function listSubscriptions(client: { request: <T>(path: string, options?: object) => Promise<T> }): Promise<Subscription[]> {
  const data = await client.request<{ subscriptions: Subscription[] }>(paths.subscriptions);
  return data.subscriptions;
}

export async function listInstances(client: { request: <T>(path: string, options?: object) => Promise<T> }): Promise<InstanceDetail[]> {
  const data = await client.request<{ instances: InstanceDetail[] }>(paths.instances);
  return data.instances;
}

export async function getInstance(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  id: string,
): Promise<InstanceDetail> {
  return client.request<InstanceDetail>(paths.instance(id));
}

export async function restartInstance(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  csrfToken: string,
  id: string,
): Promise<{ operation_id: string; status: string }> {
  return client.request(paths.restartInstance(id), { method: "POST", headers: csrfHeader(csrfToken) });
}

export async function reinstallInstance(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  csrfToken: string,
  id: string,
  imageID: string,
): Promise<{ operation_id: string; status: string }> {
  return client.request(paths.reinstallInstance(id), {
    method: "POST",
    body: { image_id: imageID },
    headers: csrfHeader(csrfToken),
  });
}

export async function getOperation(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  id: string,
): Promise<OperationDetail> {
  return client.request<OperationDetail>(paths.operation(id));
}

export async function getWallet(client: { request: <T>(path: string, options?: object) => Promise<T> }): Promise<WalletView> {
  return client.request<WalletView>(paths.wallet);
}

export async function listInvoices(client: { request: <T>(path: string, options?: object) => Promise<T> }): Promise<InvoiceDetail[]> {
  const data = await client.request<{ invoices: InvoiceDetail[] }>(paths.invoices);
  return data.invoices;
}

export async function getInvoice(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  id: string,
): Promise<InvoiceDetail> {
  return client.request<InvoiceDetail>(paths.invoice(id));
}

export async function listNotifications(client: { request: <T>(path: string, options?: object) => Promise<T> }): Promise<NotificationList> {
  return client.request<NotificationList>(paths.notifications);
}

export async function markNotificationRead(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  csrfToken: string,
  id: string,
): Promise<void> {
  await client.request(paths.markNotificationRead(id), { method: "POST", headers: csrfHeader(csrfToken) });
}

export async function listTickets(client: { request: <T>(path: string, options?: object) => Promise<T> }): Promise<TicketDetail[]> {
  const data = await client.request<{ tickets: TicketDetail[] }>(paths.tickets);
  return data.tickets;
}

export async function getTicket(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  id: string,
): Promise<TicketDetail> {
  return client.request<TicketDetail>(paths.ticket(id));
}

export async function createTicket(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  csrfToken: string,
  input: { subject: string; priority: string; message: string },
): Promise<{ id: string; ticket_no: string }> {
  return client.request(paths.tickets, { method: "POST", body: input, headers: csrfHeader(csrfToken) });
}

export async function addTicketMessage(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  csrfToken: string,
  id: string,
  message: string,
): Promise<void> {
  await client.request(paths.ticketMessages(id), {
    method: "POST",
    body: { message },
    headers: csrfHeader(csrfToken),
  });
}

export async function closeTicket(
  client: { request: <T>(path: string, options?: object) => Promise<T> },
  csrfToken: string,
  id: string,
): Promise<void> {
  await client.request(paths.ticketClose(id), { method: "POST", headers: csrfHeader(csrfToken) });
}
