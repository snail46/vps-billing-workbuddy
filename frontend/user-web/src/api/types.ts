/**
 * The user-facing API contract types.
 *
 * These mirror the payloads the backend serves. They sit next to the shared
 * envelope types: the envelope is the transport's shape, these are the
 * products' — and a field the pages need that the backend does not send is a
 * type error here, not a blank column in production.
 */

export interface User {
  id: string;
  email: string;
  display_name: string;
  status: string;
}

export interface UserSession {
  user: User;
  csrf_token: string;
  expires_at: string;
}

export interface Plan {
  id: string;
  product_id: string;
  slug: string;
  name_i18n: Record<string, string>;
  status: string;
  memory_mb: number;
  disk_gb: number;
  traffic_gb: number | null;
  bandwidth_mbps: number | null;
  ipv4_count: number;
  ipv6_count: number;
  nat_port_count: number;
  virtualization: string;
  billing_cycle: string;
  price_minor: number;
  currency: string;
}

export interface Product {
  id: string;
  slug: string;
  name_i18n: Record<string, string>;
  description_i18n: Record<string, string>;
  status: string;
  plans: Plan[];
}

export interface OrderItem {
  id: string;
  plan_id: string;
  quantity: number;
  unit_price_minor: number;
  total_minor: number;
  plan_snapshot: { name_i18n?: Record<string, string>; billing_cycle?: string };
}

export interface Order {
  id: string;
  order_no: string;
  status: string;
  subtotal_minor: number;
  discount_minor: number;
  total_minor: number;
  currency: string;
  paid_at: string | null;
  created_at: string;
  items: OrderItem[];
}

export interface PaymentIntent {
  payment_no: string;
  order_id: string;
  gateway: string;
  status: string;
  amount_minor: number;
  currency: string;
  gateway_payment_id: string;
  pay_url: string;
  expires_at: string;
}

export interface Subscription {
  id: string;
  status: string;
  plan_snapshot: { name_i18n?: Record<string, string> } | null;
  current_period_start: string | null;
  current_period_end: string | null;
  created_at: string;
}

export interface Instance {
  id: string;
  subscription_id: string;
  name: string;
  desired_state: string;
  observed_state: string;
  cpu_cores: number;
  memory_mb: number;
  disk_gb: number;
  provider_instance_id: string | null;
  last_synced_at: string | null;
  created_at: string;
}

export interface InstanceNetwork {
  type: string;
  address: string | null;
  gateway: string | null;
  prefix: number | null;
}

export interface InstancePortForward {
  protocol: string;
  public_ip: string;
  public_port: number;
  guest_port: number;
  description: string | null;
  status: string;
}

export interface InstanceTraffic {
  period_start: string;
  period_end: string;
  rx_bytes: number;
  tx_bytes: number;
  source: string;
}

export interface InstanceDetail extends Instance {
  networks: InstanceNetwork[];
  port_forwards: InstancePortForward[];
  traffic: InstanceTraffic[];
  open_operation: Operation | null;
}

export interface OperationStep {
  key: string;
  order: number;
  status: string;
  progress: number;
  attempt: number;
  error_code: string | null;
  started_at: string | null;
  finished_at: string | null;
}

export interface Operation {
  id: string;
  type: string;
  resource_type: string;
  resource_id: string;
  status: string;
  phase: string | null;
  progress: number;
  message_key: string | null;
  error_code: string | null;
  retry_count: number;
  max_retries: number;
  started_at: string | null;
  finished_at: string | null;
  created_at: string;
}

export interface OperationDetail {
  operation: Operation;
  steps: OperationStep[];
}

export interface Wallet {
  id: string;
  currency: string;
  available_balance_minor: number;
}

export interface LedgerMovement {
  transaction_type: string;
  direction: string;
  amount_minor: number;
  currency: string;
  description?: string;
  created_at: string;
}

export interface WalletView {
  wallets: Wallet[];
  ledger: LedgerMovement[];
}

export interface InvoiceItem {
  id: string;
  description_i18n: Record<string, unknown> | null;
  quantity: number;
  unit_amount_minor: number;
  total_minor: number;
}

export interface Invoice {
  id: string;
  invoice_no: string;
  status: string;
  amount_minor: number;
  currency: string;
  due_at: string | null;
  paid_at: string | null;
  created_at: string;
}

export interface InvoiceDetail {
  invoice: Invoice;
  items: InvoiceItem[];
}

export interface Notification {
  id: string;
  type: string;
  title_key: string;
  message_key: string;
  parameters: Record<string, unknown>;
  severity: string;
  read_at: string | null;
  created_at: string;
}

export interface NotificationList {
  notifications: Notification[];
  unread: number;
}

export interface Ticket {
  id: string;
  ticket_no: string;
  subject: string;
  status: string;
  priority: string;
  created_at: string;
  updated_at: string;
  closed_at: string | null;
}

export interface TicketMessage {
  id: string;
  sender_type: string;
  message: string;
  created_at: string;
}

export interface TicketDetail {
  ticket: Ticket;
  messages: TicketMessage[];
}
