import dayjs from "dayjs";
import { Invoice, InvoiceFilters, InvoiceStage, InvoiceStatus, TimelineItem } from "./types";
import { metrics } from "./metrics";
import { webhookDispatcher } from "./webhooks";

type Sorter = (invoice: Invoice) => (string | number)[];

export class InvoiceStore {
  private invoices: Invoice[];
  private sorter?: Sorter;

  constructor(seed: Invoice[]) {
    this.invoices = [...seed];
    metrics.setInvoiceVolume(this.invoices.length);
    metrics.setCapUsage(this.computeCapUsage());
  }

  private computeCapUsage(): number {
    const cap = this.invoices.reduce((acc, invoice) => acc + invoice.amount, 0);
    const capLimit = 10_000_000;
    return cap / capLimit;
  }

  all() {
    return this.applySort([...this.invoices]);
  }

  findById(id: string) {
    return this.invoices.find((invoice) => invoice.id === id);
  }

  list(filters: InvoiceFilters = {}) {
    const filtered = this.invoices.filter((invoice) => {
      if (filters.stage && invoice.stage !== filters.stage) return false;
      if (filters.branch && invoice.branch !== filters.branch) return false;
      if (filters.status && invoice.status !== filters.status) return false;
      if (filters.fundingStatus && invoice.fundingStatus !== filters.fundingStatus) return false;
      if (filters.minDate && dayjs(invoice.createdAt).isBefore(dayjs(filters.minDate))) return false;
      if (filters.maxDate && dayjs(invoice.createdAt).isAfter(dayjs(filters.maxDate))) return false;
      if (filters.minAmount && invoice.amount < filters.minAmount) return false;
      if (filters.maxAmount && invoice.amount > filters.maxAmount) return false;
      return true;
    });
    return this.applySort(filtered);
  }

  private applySort(invoices: Invoice[]) {
    if (!this.sorter) return invoices;
    return invoices.sort((a, b) => {
      const aKey = this.sorter!(a);
      const bKey = this.sorter!(b);
      for (let i = 0; i < Math.max(aKey.length, bKey.length); i += 1) {
        const aVal = aKey[i];
        const bVal = bKey[i];
        if (aVal === bVal) continue;
        if (typeof aVal === "number" && typeof bVal === "number") {
          return aVal - bVal;
        }
        return String(aVal).localeCompare(String(bVal));
      }
      return 0;
    });
  }

  sortBy(sorter: Sorter) {
    this.sorter = sorter;
    this.invoices = this.applySort(this.invoices);
  }

  updateStatus(
    id: string,
    status: InvoiceStatus,
    stage: InvoiceStage,
    actor: string,
    actorRole: string,
    note?: string,
    txHash?: string | null
  ) {
    const invoice = this.findById(id);
    if (!invoice) return null;

    const timestamp = new Date().toISOString();
    const timelineItem: TimelineItem = {
      status,
      actor,
      note: note ?? actionNote(status, actorRole),
      timestamp
    };

    invoice.status = status;
    invoice.stage = stage;
    invoice.timeline = [...invoice.timeline, timelineItem];
    invoice.updatedAt = timestamp;
    invoice.txHash = txHash ?? invoice.txHash;

    metrics.observeApprovalLatency(invoice);
    metrics.setCapUsage(this.computeCapUsage());

    webhookDispatcher.emitLifecycleEvent(invoice, timelineItem);

    return invoice;
  }
}

function actionNote(status: InvoiceStatus, role: string) {
  switch (status) {
    case "approved":
      return `${role} approval completed`;
    case "funding_confirmed":
      return "Fiat funding confirmed";
    case "rejected":
      return `${role} rejection`;
    case "escalated":
      return `${role} escalation`;
    case "signed":
      return "Voucher signed";
    case "submitted":
      return "Submitted onchain";
    default:
      return `${role} update`;
  }
}
