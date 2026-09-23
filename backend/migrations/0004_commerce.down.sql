-- 0004_commerce (down) — reverses the commercial tables.
--
-- Dropped in reverse dependency order. `orders` references `users` and is referenced
-- by `order_items`, `payments` and `invoices`; `plans` references `products` and
-- `node_groups` and is referenced by `order_items` and `subscriptions`;
-- `ledger_entries` references `ledger_transactions`.
--
-- Dropping this discards commercial history: orders, payments, invoices and the
-- whole ledger. That is why the script says so rather than being omitted — the CI
-- integration job applies the history forward and rolls it back, and a migration
-- without a `down` makes that check impossible to run. It is acceptable only because
-- the rollback happens on a database created moments earlier by the same job.
--
-- Rolling a production database back past this migration destroys the financial
-- record and is not a routine act. It is also not a recoverable one: the ledger is
-- append-only by design, so there is no other copy of it to restore from.

DROP TABLE invoice_items;
DROP TABLE invoices;
DROP TABLE subscriptions;
DROP TABLE outbox_events;
DROP TABLE ledger_entries;
DROP TABLE ledger_transactions;
DROP TABLE wallets;
DROP INDEX ux_payments_gateway_external;
DROP TABLE payments;
DROP TABLE order_items;
DROP TABLE orders;
DROP TABLE plans;
DROP TABLE products;
DROP TABLE node_groups;
