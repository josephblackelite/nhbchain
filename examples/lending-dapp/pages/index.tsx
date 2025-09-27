import Link from 'next/link';
import { StatCard } from '../components/StatCard';

const documentationLinks = [
  {
    label: 'Lending Developer Guide',
    href: '/docs',
    helper: 'Walkthrough of the lend, borrow, and repay RPC endpoints.'
  },
  {
    label: 'RPC Reference',
    href: '/docs/rpc',
    helper: 'Complete API surface for interacting with NHBChain from custom dApps.'
  },
  {
    label: 'Observability Playbooks',
    href: '/observability',
    helper: 'Monitor liquidity and borrower health across your deployment.'
  }
];

export default function HomePage() {
  return (
    <>
      <section className="hero">
        <div className="hero-card">
          <div className="tag">Phase 4 Reference Implementation</div>
          <h1 className="hero-title">Empowering the unbanked with permissionless credit rails</h1>
          <p className="hero-subtitle">
            This lending protocol, built on NHBCoin, creates a transparent and inclusive credit system. Anyone with internet
            access and ZNHB collateral can unlock NHB liquidity to grow their business, weather emergencies, or seize new
            opportunities.
          </p>
          <div className="hero-actions">
            <Link className="button-primary" href="/borrow">
              Explore Borrow Flow
            </Link>
            <Link className="button-secondary" href="/earn">
              Provide Liquidity
            </Link>
          </div>
        </div>
        <div className="hero-card" style={{ background: 'rgba(15, 23, 42, 0.86)' }}>
          <h2>Why it matters</h2>
          <p>
            By leveraging non-traditional collateral and the `lend_borrowNHBWithFee` endpoint, developers can build local lending
            desks that serve unbanked communities. Gig workers and creators turn their ZNHB stake into productive capital while
            developers capture sustainable fees.
          </p>
          <ul style={{ paddingLeft: 20, color: 'var(--color-text-muted)' }}>
            <li>Create access to credit without traditional banks or credit scores.</li>
            <li>Accept ZNHB as collateral to unlock liquidity from network participation.</li>
            <li>Launch permissionless lending businesses from anywhere in the world.</li>
          </ul>
        </div>
      </section>

      <section style={{ marginTop: 60 }}>
        <h2 className="section-title">Developer quick start</h2>
        <p className="section-description">
          Pair this Next.js project with the NHBChain docs to move from prototype to production. The code mirrors the structure of
          the developer guide so you can swap mock state for real RPC calls as you integrate wallets and backend services.
        </p>
        <div className="info-grid">
          {documentationLinks.map((link) => (
            <StatCard
              key={link.label}
              label={link.label}
              value={<Link href={link.href}>{link.href}</Link>}
              helper={link.helper}
            />
          ))}
        </div>
      </section>
    </>
  );
}
