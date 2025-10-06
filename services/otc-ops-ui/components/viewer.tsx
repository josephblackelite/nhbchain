"use client";

import dayjs from "dayjs";
import { useState } from "react";
import { ActionPayload, Invoice } from "../lib/types";

interface Props {
  invoice: Invoice | null;
  onAction: (invoice: Invoice, payload: ActionPayload) => Promise<void>;
}

const roles: ActionPayload["actorRole"][] = ["branch", "treasury", "superadmin"];

export function InvoiceViewer({ invoice, onAction }: Props) {
  const [actor, setActor] = useState("ops-user");
  const [role, setRole] = useState<ActionPayload["actorRole"]>("branch");
  const [note, setNote] = useState("");
  const [txHash, setTxHash] = useState("");

  if (!invoice) {
    return <aside className="viewer empty">Select an invoice to see the timeline.</aside>;
  }

  const actions = [
    {
      key: "approve",
      label: "Approve",
      visible: invoice.stage === "review" || invoice.stage === "approval"
    },
    {
      key: "reject",
      label: "Reject",
      visible: invoice.stage !== "completed" && invoice.stage !== "rejected"
    },
    {
      key: "escalate",
      label: "Escalate",
      visible: invoice.stage === "receipt" || invoice.stage === "review"
    },
    {
      key: "sign",
      label: "Sign",
      visible: role === "superadmin" && (invoice.stage === "approval" || invoice.stage === "funding")
    },
    {
      key: "submit",
      label: "Sign & Submit",
      visible:
        role === "superadmin" &&
        (invoice.status === "signed" || invoice.stage === "approval" || invoice.stage === "funding")
    }
  ] as const;

  const performAction = async (action: ActionPayload["action"]) => {
    await onAction(invoice, {
      action,
      actor,
      actorRole: role,
      note: note || undefined,
      txHash: txHash || undefined
    });
    setNote("");
    if (action === "submit") {
      setTxHash("");
    }
  };

  return (
    <aside className="viewer">
      <header className="viewer-header">
        <div>
          <h2>{invoice.id}</h2>
          <p>{invoice.counterparty}</p>
        </div>
        <div className={`status ${invoice.status}`}>
          {invoice.status.replace(/_/g, " ")}
        </div>
      </header>

      <section className="metadata">
        <dl>
          <div>
            <dt>Branch</dt>
            <dd>{invoice.branch}</dd>
          </div>
          <div>
            <dt>Amount</dt>
            <dd>
              {invoice.amount.toLocaleString(undefined, {
                style: "currency",
                currency: invoice.currency === "USDC" ? "USD" : invoice.currency
              })}
            </dd>
          </div>
          <div>
            <dt>Rate</dt>
            <dd>{invoice.rate.toFixed(4)}</dd>
          </div>
          <div>
            <dt>TWAP</dt>
            <dd>{invoice.twapReference.toFixed(4)}</dd>
          </div>
          <div>
            <dt>Fiat Amount</dt>
            <dd>
              {invoice.fiatAmount.toLocaleString(undefined, {
                style: "currency",
                currency: invoice.fiatCurrency || "USD"
              })}
            </dd>
          </div>
          <div>
            <dt>Funding Status</dt>
            <dd className={`funding ${invoice.fundingStatus}`}>{invoice.fundingStatus}</dd>
          </div>
          <div>
            <dt>Funding Reference</dt>
            <dd>{invoice.fundingReference ?? "Pending"}</dd>
          </div>
          <div>
            <dt>Voucher</dt>
            <dd>{invoice.voucherId}</dd>
          </div>
          <div>
            <dt>Tx Hash</dt>
            <dd>{invoice.txHash ?? "Pending"}</dd>
          </div>
        </dl>
      </section>

      <section className="evidence">
        <h3>Receipt Evidence</h3>
        <ul>
          {invoice.receiptEvidence.map((item) => (
            <li key={item.url}>
              <a href={item.url} target="_blank" rel="noreferrer">
                {item.description}
              </a>
              <span>{dayjs(item.uploadedAt).format("MMM D, HH:mm")}</span>
            </li>
          ))}
        </ul>
      </section>

      <section className="timeline">
        <h3>Timeline</h3>
        <ol>
          {invoice.timeline.map((event) => (
            <li key={`${event.timestamp}-${event.status}`}>
              <span className="timestamp">{dayjs(event.timestamp).format("MMM D, HH:mm")}</span>
              <span className="status-tag">{event.status.replace(/_/g, " ")}</span>
              <span className="note">{event.note}</span>
              <span className="actor">{event.actor}</span>
            </li>
          ))}
        </ol>
      </section>

      <section className="actions">
        <h3>Workflow Actions</h3>
        <div className="action-grid">
          {actions
            .filter((action) => action.visible)
            .map((action) => (
              <button key={action.key} onClick={() => performAction(action.key)}>
                {action.label}
              </button>
            ))}
        </div>
        <div className="action-form">
          <label>
            Actor
            <input value={actor} onChange={(event) => setActor(event.target.value)} />
          </label>
          <label>
            Role
            <select value={role} onChange={(event) => setRole(event.target.value as ActionPayload["actorRole"]) }>
              {roles.map((candidate) => (
                <option key={candidate} value={candidate}>
                  {candidate}
                </option>
              ))}
            </select>
          </label>
          <label>
            Note
            <input value={note} onChange={(event) => setNote(event.target.value)} />
          </label>
          <label>
            Tx Hash
            <input value={txHash} onChange={(event) => setTxHash(event.target.value)} />
          </label>
        </div>
      </section>
    </aside>
  );
}
