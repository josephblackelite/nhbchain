import { Invoice, TimelineItem } from "./types";

class WebhookDispatcher {
  async emitLifecycleEvent(invoice: Invoice, event: TimelineItem) {
    const endpoint = process.env.WEBHOOK_ENDPOINT;
    if (!endpoint) {
      console.warn("WEBHOOK_ENDPOINT not configured; skipping webhook emission");
      return;
    }

    const payload = {
      invoiceId: invoice.id,
      branch: invoice.branch,
      status: invoice.status,
      stage: invoice.stage,
      voucherId: invoice.voucherId,
      txHash: invoice.txHash,
      evidence: invoice.receiptEvidence,
      event,
      timeline: invoice.timeline
    };

    try {
      const response = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });

      if (!response.ok) {
        console.error(`Webhook dispatch failed: ${response.status}`);
      }
    } catch (error) {
      console.error("Webhook dispatch error", error);
    }
  }
}

export const webhookDispatcher = new WebhookDispatcher();
