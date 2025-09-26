import dayjs from "dayjs";
import client, { Counter, Gauge, Histogram } from "prom-client";
import { Invoice } from "./types";

class Metrics {
  private register = client.register;
  private invoiceVolume: Gauge<string>;
  private approvalLatency: Histogram<string>;
  private receiptUploadFailures: Counter<string>;
  private capUsage: Gauge<string>;
  private mintSuccess: Counter<string>;
  private signerHealth: Gauge<string>;

  constructor() {
    client.collectDefaultMetrics({ register: this.register });

    this.invoiceVolume = new client.Gauge({
      name: "otc_invoice_volume_total",
      help: "Total number of invoices tracked",
      registers: [this.register]
    });

    this.approvalLatency = new client.Histogram({
      name: "otc_approval_latency_seconds",
      help: "Latency from receipt to approval",
      buckets: [60, 600, 1800, 3600, 21600, 86400],
      registers: [this.register]
    });

    this.receiptUploadFailures = new client.Counter({
      name: "otc_receipt_upload_failures_total",
      help: "Count of receipt upload failures",
      registers: [this.register]
    });

    this.capUsage = new client.Gauge({
      name: "otc_cap_usage_ratio",
      help: "Ratio of used cap vs limit",
      registers: [this.register]
    });

    this.mintSuccess = new client.Counter({
      name: "otc_mint_success_total",
      help: "Successful voucher sign & submit events",
      registers: [this.register]
    });

    this.signerHealth = new client.Gauge({
      name: "otc_signer_health",
      help: "Health of signer service (1 healthy, 0 unhealthy)",
      registers: [this.register]
    });

    this.signerHealth.set(1);
  }

  setInvoiceVolume(value: number) {
    this.invoiceVolume.set(value);
  }

  setCapUsage(ratio: number) {
    this.capUsage.set(ratio);
  }

  incrementReceiptFailure() {
    this.receiptUploadFailures.inc();
  }

  recordMintSuccess() {
    this.mintSuccess.inc();
  }

  observeApprovalLatency(invoice: Invoice) {
    const approved = invoice.timeline.find((event) => event.status === "approved");
    if (!approved) return;
    const receipt = invoice.timeline.find((event) => event.status === "receipt_verified")
      ?? invoice.timeline[0];
    if (!receipt) return;
    const diff = dayjs(approved.timestamp).diff(dayjs(receipt.timestamp), "second");
    if (diff > 0) {
      this.approvalLatency.observe(diff);
    }
  }

  async metrics() {
    return this.register.metrics();
  }
}

export const metrics = new Metrics();
