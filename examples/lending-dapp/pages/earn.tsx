import Head from 'next/head';
import { EarnForm } from '../components/EarnForm';
import { StatCard } from '../components/StatCard';
import { useEarnState } from '../lib/mockData';
import { formatNumber } from '../lib/utils';

export default function EarnPage() {
  const { availableBalance, suppliedBalance, supply, withdraw } = useEarnState();

  return (
    <>
      <Head>
        <title>Earn | NHBChain Lending</title>
      </Head>
      <section className="page-heading">
        <span>Provide Liquidity</span>
        <h1>Earn sustainable NHB yield</h1>
        <p className="section-description">
          The NHBChain Lending Playbook — pattern LEND-02 — walks through this supply and withdrawal journey. Swap the mock
          state in this component for real RPC calls to connect with NHBChain pool contracts.
        </p>
      </section>

      <div className="card-grid">
        <EarnForm
          availableBalance={availableBalance}
          suppliedBalance={suppliedBalance}
          onSupply={supply}
          onWithdraw={withdraw}
        />
        <div className="form-card" style={{ gap: '16px' }}>
          <h2>Your pool position</h2>
          <div className="stat-row">
            <span>Wallet balance</span>
            <strong>{formatNumber(availableBalance)} NHB</strong>
          </div>
          <div className="stat-row">
            <span>Supplied to pool</span>
            <strong>{formatNumber(suppliedBalance)} NHB</strong>
          </div>
          <div className="stat-row">
            <span>Projected APY</span>
            <strong>8.4%</strong>
          </div>
          <p className="helper-text">
            This mock APY is configurable. In production, derive it from pool utilization or on-chain interest rate models.
          </p>
        </div>
      </div>

      <section style={{ marginTop: 48 }}>
        <h2 className="section-title">Implementation notes</h2>
        <div className="info-grid">
          <StatCard
            label="Supply RPC"
            value={<code>lend_supplyNHB</code>}
            helper="Invoke after wallet signature to increase pool liquidity."
          />
          <StatCard
            label="Withdraw RPC"
            value={<code>lend_withdrawNHB</code>}
            helper="Subtracts liquidity from the pool while updating the lender share index."
          />
          <StatCard
            label="Accounting"
            value={<code>lend_getPosition</code>}
            helper="Fetch balances and APY information to render this dashboard."
          />
        </div>
      </section>
    </>
  );
}
