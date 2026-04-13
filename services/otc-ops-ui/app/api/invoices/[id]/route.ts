import { NextResponse } from "next/server";
import { store } from "../../../../data/invoices";
import { ActionPayload, InvoiceStage, InvoiceStatus } from "../../../../lib/types";
import { metrics } from "../../../../lib/metrics";

export function GET(
  _request: Request,
  { params }: { params: { id: string } }
) {
  const invoice = store.findById(params.id);
  if (!invoice) {
    return NextResponse.json({ error: "Not found" }, { status: 404 });
  }
  return NextResponse.json({ invoice });
}

const transitions: Record<
  ActionPayload["action"],
  { status: InvoiceStatus; stage: InvoiceStage }
> = {
  approve: { status: "approved", stage: "completed" },
  reject: { status: "rejected", stage: "rejected" },
  escalate: { status: "escalated", stage: "review" },
  sign: { status: "signed", stage: "approval" },
  submit: { status: "submitted", stage: "completed" }
};

export async function PATCH(
  request: Request,
  { params }: { params: { id: string } }
) {
  const body = (await request.json()) as ActionPayload;
  const { action, actor, actorRole, txHash, note } = body;
  if (!action || !actor || !actorRole) {
    return NextResponse.json({ error: "Invalid payload" }, { status: 400 });
  }

  const mapping = transitions[action];
  if (!mapping) {
    return NextResponse.json({ error: "Unsupported action" }, { status: 400 });
  }

  if (action === "sign" && actorRole !== "superadmin") {
    return NextResponse.json({ error: "SuperAdmin required for sign" }, { status: 403 });
  }
  if (action === "submit" && actorRole !== "superadmin") {
    return NextResponse.json({ error: "SuperAdmin required for submit" }, { status: 403 });
  }

  const invoice = store.updateStatus(
    params.id,
    mapping.status,
    mapping.stage,
    actor,
    actorRole,
    note,
    txHash ?? null
  );

  if (!invoice) {
    return NextResponse.json({ error: "Not found" }, { status: 404 });
  }

  if (action === "submit") {
    metrics.recordMintSuccess();
  }

  return NextResponse.json({ invoice });
}
