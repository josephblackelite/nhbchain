import { PartnerReadinessRecord } from "../lib/types";

export const partners: PartnerReadinessRecord[] = [
  {
    id: "c7d1f2a0-4e87-4de8-8d10-19a9f2c17f5a",
    name: "Atlas Capital",
    legalName: "Atlas Capital LLC",
    status: "pending_review",
    stage: "kyb_review",
    kybUpdatedAt: "2024-03-11T15:30:00Z",
    dossierKey: "partners/atlas/kyb/2024-03-11.zip",
    licensingKey: "partners/atlas/licensing/2024-03-11.pdf",
    contacts: [
      { name: "Alice Chen", email: "alice@atlas.example", role: "Partner Admin", phone: "+1-555-0182" },
      { name: "Jared Cole", email: "jared@atlas.example", role: "Compliance" }
    ],
    notes: "Awaiting final review sign-off from root admin."
  },
  {
    id: "c5a439e8-35f3-4b22-81fd-4f31288b9c02",
    name: "Helios Partners",
    legalName: "Helios Partners AG",
    status: "approved",
    stage: "ready",
    kybUpdatedAt: "2024-02-27T09:10:00Z",
    approvalUpdatedAt: "2024-02-28T18:45:00Z",
    dossierKey: "partners/helios/kyb/2024-02-27.tar.gz",
    licensingKey: "partners/helios/licensing/2024-02-24.pdf",
    contacts: [
      { name: "Sven Richter", email: "sven@helios.example", role: "Operations" },
      { name: "Maya Patel", email: "maya@helios.example", role: "Compliance" }
    ],
    notes: "Ready for production minting; maintain quarterly KYB refresh cadence."
  },
  {
    id: "9c3c37d0-6f25-40b9-8a7f-6a8f27f0d6b1",
    name: "NovaBridge Markets",
    legalName: "NovaBridge Markets Pte Ltd",
    status: "pending_documents",
    stage: "application",
    kybUpdatedAt: "2024-03-03T12:00:00Z",
    dossierKey: "partners/novabridge/kyb/2024-03-03.zip",
    licensingKey: "partners/novabridge/licensing/placeholder.pdf",
    contacts: [
      { name: "Priya Iyer", email: "priya@novabridge.example", role: "Partner Admin" }
    ],
    notes: "Outstanding: proof of licensing in Singapore."
  }
];
