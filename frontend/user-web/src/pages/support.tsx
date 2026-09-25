/**
 * Support and account screens: tickets (list, open, thread, close) and the
 * account page. A conversation is the customer's own; every write carries
 * their CSRF token.
 */

import { Button, Card, ErrorState, LoadingState, StatusBadge, type ApiClient } from "@vps/shared";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { useMutation, useQuery } from "../hooks.js";
import { useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";

import { closeTicket, createTicket, getTicket, listTickets, addTicketMessage } from "../api/endpoints.js";
import { useDate } from "../format-hooks.js";
import { Page } from "../page-fragments.js";
import { useSession } from "../session-context.js";

export function TicketsPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const date = useDate();
  const queryClient = useQueryClient();
  const { session } = useSession();
  const [subject, setSubject] = useState("");
  const [message, setMessage] = useState("");
  const [priority, setPriority] = useState("normal");

  const query = useQuery({ queryKey: ["/api/v1/tickets"], queryFn: () => listTickets(apiClient) });
  const create = useMutation({
    mutationFn: () => createTicket(apiClient, session?.csrf_token ?? "", { subject, priority, message }),
    onSuccess: () => {
      setSubject("");
      setMessage("");
      return queryClient.invalidateQueries({ queryKey: ["/api/v1/tickets"] });
    },
  });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} />;
  }

  return (
    <Page title={t("user.tickets.title")}>
      {query.data.length === 0 && <Card>{t("user.tickets.empty")}</Card>}
      <div className="space-y-2">
        {query.data.map((detail) => (
          <Card
            key={detail.ticket.id}
            title={detail.ticket.subject}
            action={
              <StatusBadge
                tone={detail.ticket.status === "closed" ? "neutral" : detail.ticket.status === "answered" ? "info" : "warning"}
                labelKey={`user.tickets.status.${detail.ticket.status}`}
              />
            }
          >
            <p className="text-xs text-content-muted">
              {t("user.tickets.no")}: {detail.ticket.ticket_no} · {t("user.tickets.priority")}: {t(`user.tickets.priority.${detail.ticket.priority}`)} · {date(detail.ticket.created_at)}
            </p>
            <Link to={`/tickets/${detail.ticket.id}`} className="text-sm underline text-content">
              {t("user.tickets.reply")}
            </Link>
          </Card>
        ))}
      </div>
      <Card title={t("user.tickets.new")}>
        <div className="space-y-2">
          <input
            value={subject}
            onChange={(event) => setSubject(event.target.value)}
            placeholder={t("user.tickets.subject")}
            className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content"
          />
          <select
            value={priority}
            onChange={(event) => setPriority(event.target.value)}
            className="rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content"
          >
            <option value="low">{t("user.tickets.priority.low")}</option>
            <option value="normal">{t("user.tickets.priority.normal")}</option>
            <option value="high">{t("user.tickets.priority.high")}</option>
          </select>
          <textarea
            value={message}
            onChange={(event) => setMessage(event.target.value)}
            placeholder={t("user.tickets.message")}
            rows={4}
            className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content"
          />
          <Button
            disabled={subject === "" || message === "" || create.isPending}
            onClick={() => void create.mutateAsync()}
          >
            {t("user.tickets.send")}
          </Button>
          {create.isError && <p className="text-sm text-status-danger-content">{t(create.error.messageKey)}</p>}
        </div>
      </Card>
    </Page>
  );
}

export function TicketDetailPage({ apiClient }: { apiClient: ApiClient }): ReactNode {
  const { t } = useTranslation();
  const date = useDate();
  const params = useParams();
  const queryClient = useQueryClient();
  const { session } = useSession();
  const [reply, setReply] = useState("");

  const query = useQuery({
    queryKey: [`/api/v1/tickets/${params.ticketID}`],
    queryFn: () => getTicket(apiClient, params.ticketID ?? ""),
  });
  const send = useMutation({
    mutationFn: () => addTicketMessage(apiClient, session?.csrf_token ?? "", params.ticketID ?? "", reply),
    onSuccess: () => {
      setReply("");
      return queryClient.invalidateQueries({ queryKey: [`/api/v1/tickets/${params.ticketID}`] });
    },
  });
  const close = useMutation({
    mutationFn: () => closeTicket(apiClient, session?.csrf_token ?? "", params.ticketID ?? ""),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: [`/api/v1/tickets/${params.ticketID}`] }),
  });

  if (query.isPending) {
    return <LoadingState />;
  }
  if (query.isError) {
    return <ErrorState messageKey={query.error.messageKey} requestId={query.error.requestId} />;
  }
  const { ticket, messages } = query.data;

  const senderName = (type: string) =>
    type === "admin" ? t("user.tickets.admin") : type === "system" ? t("user.tickets.system") : t("user.tickets.you");

  return (
    <Page title={ticket.subject} subtitle={`${t("user.tickets.no")}: ${ticket.ticket_no}`}>
      <Card>
        <ul className="space-y-3">
          {messages.map((message) => (
            <li key={message.id} className="rounded-md border border-border-subtle bg-surface-muted px-3 py-2">
              <p className="text-xs text-content-muted">
                {senderName(message.sender_type)} · {date(message.created_at)}
              </p>
              <p className="whitespace-pre-wrap text-sm text-content">{message.message}</p>
            </li>
          ))}
        </ul>
      </Card>
      {ticket.status !== "closed" ? (
        <Card>
          <div className="space-y-2">
            <textarea
              value={reply}
              onChange={(event) => setReply(event.target.value)}
              placeholder={t("user.tickets.replyPlaceholder")}
              rows={3}
              className="w-full rounded-md border border-border-subtle bg-surface px-2 py-1.5 text-sm text-content"
            />
            <div className="flex items-center gap-2">
              <Button disabled={reply === "" || send.isPending} onClick={() => void send.mutateAsync()}>
                {t("user.tickets.reply")}
              </Button>
              <Button variant="secondary" disabled={close.isPending} onClick={() => void close.mutateAsync()}>
                {t("user.tickets.close")}
              </Button>
            </div>
            {send.isError && <p className="text-sm text-status-danger-content">{t(send.error.messageKey)}</p>}
          </div>
        </Card>
      ) : (
        <Card>{t("user.tickets.closed")}</Card>
      )}
    </Page>
  );
}

export function AccountPage(): ReactNode {
  const { t } = useTranslation();
  const date = useDate();
  const { session, signOut } = useSession();
  if (session === null) {
    return <Card>{t("common.state.permissionDenied.title")}</Card>;
  }

  return (
    <Page title={t("user.account.title")}>
      <Card title={t("user.account.signedInAs")}>
        <div className="space-y-1 text-sm text-content">
          <p>
            {t("user.account.email")}: {session.user.email}
          </p>
          <p>
            {t("user.account.status")}: {session.user.status}
          </p>
          <p>
            {t("user.account.session")}: {date(session.expires_at)}
          </p>
          <Button variant="secondary" onClick={() => void signOut()}>
            {t("user.account.signOut")}
          </Button>
        </div>
      </Card>
    </Page>
  );
}
