import Head from 'next/head';
import Link from 'next/link';

const sampleSkills = [
  {
    skill: 'Solidity Auditing',
    issuer: '0xVerifier',
    issued: '2024-01-14',
    expires: '2024-07-14'
  },
  {
    skill: 'Figma Prototyping',
    issuer: '0xStudio',
    issued: '2024-01-01',
    expires: '—'
  }
];

export default function Skills() {
  return (
    <>
      <Head>
        <title>Skill verification</title>
      </Head>
      <main>
        <section>
          <h1>Skill verification ledger</h1>
          <p>
            Verifiers sign off capability badges. The <code>reputation_verifySkill</code> RPC is retired, so the attestations
            below are sample data that product teams can use to develop review pipelines, UI affordances, and governance
            approval loops.
          </p>
          <Link href="/">Back to board</Link>
        </section>

        <section>
          <h2>Recent attestations</h2>
          <div className="card-grid">
            {sampleSkills.map((item) => (
              <article key={item.skill} className="card">
                <h3>{item.skill}</h3>
                <p style={{ fontSize: '0.9rem', opacity: 0.7 }}>Issuer: {item.issuer}</p>
                <p style={{ fontSize: '0.9rem', opacity: 0.7 }}>Issued: {item.issued}</p>
                <p style={{ fontSize: '0.9rem', opacity: 0.7 }}>Expires: {item.expires}</p>
              </article>
            ))}
          </div>
        </section>

        <section>
          <h2>Verifier responsibilities</h2>
          <ul>
            <li>Maintain artefacts (code reviews, interviews, delivery checklists) for every attestation.</li>
            <li>Schedule periodic re-evaluations for expiring skills.</li>
            <li>Collaborate with escrow committees to suspend retainers if verified skills are withdrawn.</li>
          </ul>
        </section>
      </main>
    </>
  );
}
