import Head from 'next/head';
import Link from 'next/link';

const milestoneLegs = [
  {
    title: 'Discovery workshop',
    type: 'deliverable',
    summary: 'Kick-off session covering requirements, success metrics, and risk register.',
    deadline: '2024-02-12',
    status: 'pending'
  },
  {
    title: 'Prototype delivery',
    type: 'deliverable',
    summary: 'Interactive demo deployed to staging. Includes QA walkthrough.',
    deadline: '2024-02-26',
    status: 'funded'
  },
  {
    title: 'Ongoing product triage',
    type: 'timebox',
    summary: 'Weekly retainer for bug triage and stakeholder syncs.',
    deadline: 'Renews monthly',
    status: 'active'
  }
];

export default function Home() {
  return (
    <>
      <Head>
        <title>Freelance board</title>
      </Head>
      <main>
        <section>
          <h1>Freelance board demo</h1>
          <p>
            This lightweight Next.js showcase illustrates how milestone-based escrows, subscriptions, and skill verification
            can be orchestrated from a single client surface. The RPC layer is wired in a stub mode so that integrators can
            iterate on UX flows while the on-chain state machine is finalised.
          </p>
          <div style={{ marginTop: '1.25rem' }}>
            <Link href="/subscriptions"><button>Subscription journey</button></Link>
            <span style={{ display: 'inline-block', width: '1rem' }}></span>
            <Link href="/skills"><button>Skill verification</button></Link>
          </div>
        </section>

        <section>
          <h2>Project milestone timeline</h2>
          <p>
            Each leg represents a single escrow sub-account. Funding, release, and cancellation map directly onto RPC calls:
            <code>escrow_milestoneFund</code>, <code>escrow_milestoneRelease</code>, and <code>escrow_milestoneCancel</code>.
          </p>
          <div className="card-grid">
            {milestoneLegs.map((leg) => (
              <article key={leg.title} className="card">
                <h3>{leg.title}</h3>
                <p style={{ textTransform: 'capitalize', opacity: 0.8 }}>{leg.type}</p>
                <p>{leg.summary}</p>
                <p style={{ fontSize: '0.9rem', opacity: 0.7 }}>Deadline: {leg.deadline}</p>
                <p style={{ fontSize: '0.9rem', opacity: 0.7 }}>Status: {leg.status}</p>
              </article>
            ))}
          </div>
        </section>

        <section>
          <h2>Event stream</h2>
          <p>
            The UI subscribes to websocket relays for <code>escrow.milestone.*</code> topics. Deadlines trigger leg due prompts,
            ensuring the payer files disputes or releases funds on time.
          </p>
          <ul>
            <li>escrow.milestone.created – project initialised</li>
            <li>escrow.milestone.funded – a leg becomes active</li>
            <li>escrow.milestone.released – payee receives funds</li>
            <li>escrow.milestone.cancelled – payer aborted remaining scope</li>
            <li>escrow.milestone.leg_due – reminder to escalate or extend</li>
          </ul>
        </section>
      </main>
    </>
  );
}
