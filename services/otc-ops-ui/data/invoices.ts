import dayjs from "dayjs";
import { Invoice, InvoiceStage } from "../lib/types";
import { InvoiceStore } from "../lib/invoiceStore";

const sampleInvoices: Invoice[] = [
  {
    id: "INV-2024-0001",
    branch: "NYC",
    counterparty: "Atlas Macro Fund",
    amount: 2500000,
    currency: "USDC",
    status: "receipt_pending",
    stage: "receipt",
    rate: 1.004,
    twapReference: 1.002,
    voucherId: "VCHR-1001",
    txHash: null,
    receiptEvidence: [
      {
        url: "https://example.com/evidence/inv-2024-0001-receipt.pdf",
        description: "Bank receipt",
        uploadedAt: dayjs().subtract(3, "day").toISOString()
      }
    ],
    timeline: [
      {
        status: "receipt_pending",
        note: "Receipt pending upload",
        actor: "ops-bot",
        timestamp: dayjs().subtract(3, "day").toISOString()
      }
    ],
    createdAt: dayjs().subtract(3, "day").toISOString(),
    updatedAt: dayjs().subtract(3, "day").toISOString(),
    capBucket: "USD",
    receiptDueAt: dayjs().subtract(1, "day").toISOString()
  },
  {
    id: "INV-2024-0002",
    branch: "LDN",
    counterparty: "Sierra Digital",
    amount: 750000,
    currency: "USDT",
    status: "under_review",
    stage: "review",
    rate: 0.998,
    twapReference: 0.997,
    voucherId: "VCHR-1002",
    txHash: null,
    receiptEvidence: [
      {
        url: "https://example.com/evidence/inv-2024-0002.png",
        description: "Portal upload",
        uploadedAt: dayjs().subtract(2, "day").toISOString()
      }
    ],
    timeline: [
      {
        status: "receipt_verified",
        note: "Receipt verified by branch ops",
        actor: "nyc-ops-1",
        timestamp: dayjs().subtract(2, "day").toISOString()
      },
      {
        status: "under_review",
        note: "Awaiting treasury approvals",
        actor: "treasury-bot",
        timestamp: dayjs().subtract(1, "day").toISOString()
      }
    ],
    createdAt: dayjs().subtract(4, "day").toISOString(),
    updatedAt: dayjs().subtract(1, "day").toISOString(),
    capBucket: "EUR",
    receiptDueAt: dayjs().subtract(2, "day").toISOString()
  },
  {
    id: "INV-2024-0003",
    branch: "SGP",
    counterparty: "Helios Partners",
    amount: 1200000,
    currency: "USDC",
    status: "awaiting_approval",
    stage: "approval",
    rate: 1.001,
    twapReference: 1.0005,
    voucherId: "VCHR-1003",
    txHash: null,
    receiptEvidence: [
      {
        url: "https://example.com/evidence/inv-2024-0003.pdf",
        description: "Invoice PDF",
        uploadedAt: dayjs().subtract(5, "day").toISOString()
      }
    ],
    timeline: [
      {
        status: "receipt_verified",
        note: "Verified by branch",
        actor: "sgp-ops-2",
        timestamp: dayjs().subtract(4, "day").toISOString()
      },
      {
        status: "under_review",
        note: "Risk review completed",
        actor: "risk-analyst-3",
        timestamp: dayjs().subtract(3, "day").toISOString()
      },
      {
        status: "awaiting_approval",
        note: "Ready for approval",
        actor: "treasury-duty",
        timestamp: dayjs().subtract(1, "day").toISOString()
      }
    ],
    createdAt: dayjs().subtract(6, "day").toISOString(),
    updatedAt: dayjs().subtract(1, "day").toISOString(),
    capBucket: "USD",
    receiptDueAt: dayjs().subtract(5, "day").toISOString()
  }
];

export const store = new InvoiceStore(sampleInvoices);

export function seedDemoData() {
  const stageOrder: Record<InvoiceStage, number> = {
    receipt: 0,
    review: 1,
    approval: 2,
    completed: 3,
    rejected: 4
  };
  store.sortBy((invoice) => [stageOrder[invoice.stage], -dayjs(invoice.updatedAt).valueOf()]);
}

seedDemoData();
