export type InvoiceStage = "receipt" | "review" | "approval" | "completed" | "rejected";

export type InvoiceStatus =
  | "receipt_pending"
  | "receipt_verified"
  | "under_review"
  | "awaiting_approval"
  | "approved"
  | "rejected"
  | "escalated"
  | "signed"
  | "submitted";

export interface EvidenceItem {
  url: string;
  description: string;
  uploadedAt: string;
}

export interface TimelineItem {
  status: InvoiceStatus;
  note: string;
  actor: string;
  timestamp: string;
}

export interface Invoice {
  id: string;
  branch: string;
  counterparty: string;
  amount: number;
  currency: string;
  status: InvoiceStatus;
  stage: InvoiceStage;
  rate: number;
  twapReference: number;
  voucherId: string;
  txHash: string | null;
  receiptEvidence: EvidenceItem[];
  timeline: TimelineItem[];
  createdAt: string;
  updatedAt: string;
  capBucket: string;
  receiptDueAt: string;
}

export interface InvoiceFilters {
  branch?: string;
  status?: InvoiceStatus;
  minDate?: string;
  maxDate?: string;
  minAmount?: number;
  maxAmount?: number;
  stage?: InvoiceStage;
}

export interface ActionPayload {
  action: "approve" | "reject" | "escalate" | "sign" | "submit";
  actor: string;
  actorRole: "branch" | "treasury" | "superadmin" | "system";
  txHash?: string;
  note?: string;
}

export type PartnerStage = "application" | "kyb_review" | "ready" | "suspended";

export type PartnerStatus = "pending_documents" | "pending_review" | "approved" | "rejected";

export interface PartnerContact {
  name: string;
  email: string;
  role: string;
  phone?: string;
}

export interface PartnerReadinessRecord {
  id: string;
  name: string;
  legalName: string;
  status: PartnerStatus;
  stage: PartnerStage;
  kybUpdatedAt: string;
  approvalUpdatedAt?: string;
  dossierKey: string;
  licensingKey: string;
  contacts: PartnerContact[];
  notes?: string;
}
