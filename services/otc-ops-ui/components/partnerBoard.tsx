import { PartnerReadinessRecord } from "../lib/types";

interface PartnerBoardProps {
  partners: PartnerReadinessRecord[];
}

function formatDate(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return value;
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit"
  });
}

export function PartnerBoard({ partners }: PartnerBoardProps) {
  return (
    <section className="partner-board">
      <div className="partner-board-header">
        <div>
          <h2>Partner Readiness</h2>
          <p>Track KYB dossier freshness and approval gates for each counterparty.</p>
        </div>
      </div>
      <div className="partner-cards">
        {partners.map((partner) => {
          const primaryContact = partner.contacts[0];
          return (
            <article key={partner.id} className="partner-card">
              <header className="partner-card-header">
                <div>
                  <h3>{partner.name}</h3>
                  <span className="partner-legal">{partner.legalName}</span>
                </div>
                <span className={`status ${partner.status}`}>{partner.status.replace(/_/g, " ")}</span>
              </header>
              <dl className="partner-metadata">
                <div>
                  <dt>Stage</dt>
                  <dd>{partner.stage.replace(/_/g, " ")}</dd>
                </div>
                <div>
                  <dt>KYB Updated</dt>
                  <dd>{formatDate(partner.kybUpdatedAt)}</dd>
                </div>
                <div>
                  <dt>Approval</dt>
                  <dd>{formatDate(partner.approvalUpdatedAt)}</dd>
                </div>
                <div>
                  <dt>Primary Contact</dt>
                  <dd>
                    {primaryContact ? (
                      <>
                        <span>{primaryContact.name}</span>
                        <span className="partner-contact">{primaryContact.email}</span>
                      </>
                    ) : (
                      "—"
                    )}
                  </dd>
                </div>
              </dl>
              <div className="partner-files">
                <span title="KYB dossier key">{partner.dossierKey}</span>
                <span title="Licensing artifact key">{partner.licensingKey}</span>
              </div>
              {partner.notes && <p className="partner-notes">{partner.notes}</p>}
            </article>
          );
        })}
      </div>
    </section>
  );
}
