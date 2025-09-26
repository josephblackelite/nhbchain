"use client";

import dayjs from "dayjs";
import { Invoice } from "../lib/types";

interface Props {
  invoices: Invoice[];
  loading: boolean;
  selectedId: string | null;
  onSelect: (invoice: Invoice) => void;
}

export function InvoiceTable({ invoices, loading, selectedId, onSelect }: Props) {
  if (loading) {
    return <div className="table">Loading queue…</div>;
  }

  if (!invoices.length) {
    return <div className="table empty">No invoices match the filters.</div>;
  }

  return (
    <div className="table">
      <table>
        <thead>
          <tr>
            <th>Invoice</th>
            <th>Counterparty</th>
            <th>Branch</th>
            <th>Amount</th>
            <th>Status</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          {invoices.map((invoice) => (
            <tr
              key={invoice.id}
              className={selectedId === invoice.id ? "selected" : ""}
              onClick={() => onSelect(invoice)}
            >
              <td>{invoice.id}</td>
              <td>{invoice.counterparty}</td>
              <td>{invoice.branch}</td>
              <td>
                {invoice.amount.toLocaleString(undefined, {
                  style: "currency",
                  currency: invoice.currency === "USDC" ? "USD" : invoice.currency
                })}
              </td>
              <td>{invoice.status.replace(/_/g, " ")}</td>
              <td>{dayjs(invoice.updatedAt).format("MMM D, HH:mm")}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
