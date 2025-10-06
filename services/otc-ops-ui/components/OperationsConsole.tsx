"use client";

import dayjs from "dayjs";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ActionPayload, Invoice, InvoiceFilters, InvoiceStage } from "../lib/types";
import { InvoiceFiltersForm } from "./filters";
import { InvoiceTable } from "./table";
import { InvoiceViewer } from "./viewer";
import { PartnerBoard } from "./partnerBoard";
import { partners as partnerData } from "../data/partners";

const stages: { key: InvoiceStage; label: string }[] = [
  { key: "receipt", label: "Receipt" },
  { key: "review", label: "Review" },
  { key: "approval", label: "Approval" },
  { key: "completed", label: "Completed" },
  { key: "rejected", label: "Rejected" }
];

export function OperationsConsole() {
  const partnerReadiness = partnerData;

  const [filters, setFilters] = useState<InvoiceFilters>({ stage: "receipt" });
  const [invoices, setInvoices] = useState<Invoice[]>([]);
  const [selected, setSelected] = useState<Invoice | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadInvoices = useCallback(async (nextFilters: InvoiceFilters) => {
    setLoading(true);
    setError(null);
    const params = new URLSearchParams();
    Object.entries(nextFilters).forEach(([key, value]) => {
      if (value === undefined || value === null || value === "") return;
      params.append(key, String(value));
    });
    try {
      const response = await fetch(`/api/invoices?${params.toString()}`);
      const data = await response.json();
      setInvoices(data.invoices ?? []);
      if (selected) {
        const updated = data.invoices?.find((invoice: Invoice) => invoice.id === selected.id);
        setSelected(updated ?? null);
      }
    } catch (err) {
      console.error(err);
      setError("Failed to load invoices");
    } finally {
      setLoading(false);
    }
  }, [selected]);

  useEffect(() => {
    loadInvoices(filters);
  }, [filters, loadInvoices]);

  const onStageChange = (stage: InvoiceStage) => {
    const next = { ...filters, stage };
    setFilters(next);
  };

  const onFilterChange = (partial: Partial<InvoiceFilters>) => {
    const next = { ...filters, ...partial };
    setFilters(next);
  };

  const onExport = () => {
    const params = new URLSearchParams();
    Object.entries(filters).forEach(([key, value]) => {
      if (value === undefined || value === null || value === "") return;
      params.append(key, String(value));
    });
    window.open(`/api/invoices/export?${params.toString()}`, "_blank");
  };

  const onAction = async (invoice: Invoice, payload: ActionPayload) => {
    try {
      const response = await fetch(`/api/invoices/${invoice.id}`, {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json"
        },
        body: JSON.stringify(payload)
      });
      if (!response.ok) {
        const body = await response.json();
        throw new Error(body.error ?? "Action failed");
      }
      const { invoice: updated } = await response.json();
      setSelected(updated);
      await loadInvoices(filters);
    } catch (err) {
      console.error(err);
      setError(err instanceof Error ? err.message : "Action failed");
    }
  };

  const summary = useMemo(() => {
    if (!invoices.length) {
      return { total: 0, notional: 0, dueToday: 0 };
    }
    const total = invoices.length;
    const notional = invoices.reduce((acc, invoice) => acc + invoice.amount, 0);
    const dueToday = invoices.filter((invoice) =>
      dayjs(invoice.receiptDueAt).isSame(dayjs(), "day")
    ).length;
    return { total, notional, dueToday };
  }, [invoices]);

  return (
    <main>
      <header className="header">
        <div>
          <h1>OTC Operations Console</h1>
          <p className="subtitle">Control center for OTC invoice lifecycle and mint readiness.</p>
        </div>
        <div className="metrics">
          <div className="metric-card">
            <span className="metric-label">Invoices</span>
            <span className="metric-value">{summary.total}</span>
          </div>
          <div className="metric-card">
            <span className="metric-label">Notional</span>
            <span className="metric-value">
              {summary.notional.toLocaleString(undefined, { style: "currency", currency: "USD" })}
            </span>
          </div>
          <div className="metric-card">
            <span className="metric-label">Due Today</span>
            <span className="metric-value">{summary.dueToday}</span>
          </div>
        </div>
      </header>

      <PartnerBoard partners={partnerReadiness} />

      <section className="stage-tabs">
        {stages.map((stage) => (
          <button
            key={stage.key}
            className={filters.stage === stage.key ? "tab active" : "tab"}
            onClick={() => onStageChange(stage.key)}
          >
            {stage.label}
          </button>
        ))}
        <button className="export" onClick={onExport}>
          Export CSV
        </button>
      </section>

      <InvoiceFiltersForm filters={filters} onChange={onFilterChange} />

      {error && <div className="error">{error}</div>}

      <div className="content">
        <InvoiceTable
          invoices={invoices}
          loading={loading}
          onSelect={setSelected}
          selectedId={selected?.id ?? null}
        />
        <InvoiceViewer invoice={selected} onAction={onAction} />
      </div>
    </main>
  );
}
