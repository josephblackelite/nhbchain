"use client";

import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import { useMemo } from "react";
import { Invoice } from "../lib/types";

dayjs.extend(relativeTime);

interface Props {
  invoices: Invoice[];
}

export function ComplianceDashboard({ invoices }: Props) {
  const summary = useMemo(() => {
    const tally: Record<"pending" | "confirmed" | "rejected", number> = {
      pending: 0,
      confirmed: 0,
      rejected: 0
    };
    let pendingNotional = 0;
    let confirmedVolume = 0;
    let totalNotional = 0;

    invoices.forEach((invoice) => {
      totalNotional += invoice.amount;
      tally[invoice.fundingStatus] += 1;
      if (invoice.fundingStatus === "pending") {
        pendingNotional += invoice.amount;
      }
      if (invoice.fundingStatus === "confirmed") {
        confirmedVolume += invoice.fiatAmount || invoice.amount;
      }
    });

    const pendingQueue = invoices
      .filter((invoice) => invoice.fundingStatus === "pending")
      .sort((a, b) => dayjs(a.updatedAt).valueOf() - dayjs(b.updatedAt).valueOf());

    const reconciliation = totalNotional === 0 ? 0 : confirmedVolume / totalNotional;

    return {
      tally,
      pendingNotional,
      confirmedVolume,
      reconciliation,
      pendingQueue
    };
  }, [invoices]);

  return (
    <section className="compliance-dashboard">
      <header>
        <div>
          <h2>Funding Assurance</h2>
          <p>
            Compliance visibility into fiat settlement confirmations, outstanding wires, and
            reconciliation progress prior to mint submission.
          </p>
        </div>
      </header>

      <div className="compliance-grid">
        <article className="compliance-card">
          <span className="label">Pending confirmations</span>
          <span className="value">{summary.tally.pending}</span>
          <span className="detail">
            ${summary.pendingNotional.toLocaleString(undefined, { maximumFractionDigits: 0 })} awaiting
            attestation
          </span>
        </article>
        <article className="compliance-card">
          <span className="label">Confirmed volume</span>
          <span className="value">
            ${summary.confirmedVolume.toLocaleString(undefined, { maximumFractionDigits: 0 })}
          </span>
          <span className="detail">Across {summary.tally.confirmed} dossiers</span>
        </article>
        <article className="compliance-card">
          <span className="label">Reconciliation ratio</span>
          <span className="value">
            {(summary.reconciliation * 100).toLocaleString(undefined, {
              maximumFractionDigits: 1
            })}
            %
          </span>
          <span className="detail">Confirmed vs. total notional</span>
        </article>
        <article className="compliance-card">
          <span className="label">Rejected wires</span>
          <span className="value">{summary.tally.rejected}</span>
          <span className="detail">Requires manual escalation</span>
        </article>
      </div>

      <div className="compliance-queue">
        <div className="queue-header">
          <h3>Pending funding checklist</h3>
          <span>{summary.pendingQueue.length} dossiers awaiting confirmation</span>
        </div>
        <ul>
          {summary.pendingQueue.slice(0, 5).map((invoice) => (
            <li key={invoice.id}>
              <div>
                <strong>{invoice.id}</strong>
                <span>{invoice.branch}</span>
              </div>
              <div>
                <span>
                  {invoice.amount.toLocaleString(undefined, {
                    style: "currency",
                    currency: invoice.currency === "USDC" ? "USD" : invoice.currency
                  })}
                </span>
                <span className="timestamp">Updated {dayjs(invoice.updatedAt).fromNow()}</span>
              </div>
            </li>
          ))}
          {summary.pendingQueue.length === 0 && (
            <li className="empty">All submitted invoices have verified fiat funding.</li>
          )}
        </ul>
      </div>
    </section>
  );
}
