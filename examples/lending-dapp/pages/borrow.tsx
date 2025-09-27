import Head from 'next/head';
import { BorrowForm } from '../components/BorrowForm';
import { StatCard } from '../components/StatCard';
import { getBorrowInsights, useBorrowState } from '../lib/mockData';
import { formatNumber } from '../lib/utils';

export default function BorrowPage() {
  const { collateral, debt, deposit, borrow, repay, healthFactor } = useBorrowState();
  const insights = getBorrowInsights(collateral);

  return (
    <>
      <Head>
        <title>Borrow | NHBChain Lending</title>
      </Head>
      <section className="page-heading">
        <span>Access Credit</span>
        <h1>Borrow NHB against your ZNHB</h1>
        <p className="section-description">
          CODEx — LEND-UI-1 maps directly to this page. Use the developer fee recipient field to wire your monetization strategy
          into the `lend_borrowNHBWithFee` endpoint and ship a sustainable lending product.
        </p>
      </section>

      <BorrowForm
        collateral={collateral}
        debt={debt}
        healthFactor={healthFactor}
        onDeposit={deposit}
        onBorrow={(amount, feeRecipient) => {
          console.info('Borrow with fee', { amount, feeRecipient });
          borrow(amount);
        }}
        onRepay={(amount) => {
          console.info('Repay loan', { amount });
          repay(amount);
        }}
      />

      <section style={{ marginTop: 48 }}>
        <h2 className="section-title">Borrower insights</h2>
        <div className="info-grid">
          <StatCard
            label="Maximum borrowable NHB"
            value={`${formatNumber(insights.maxBorrow)} NHB`}
            helper="Calculated from the 75% utilization target."
          />
          <StatCard
            label="Liquidation threshold"
            value={`${formatNumber(insights.liquidationPrice)} ZNHB`}
            helper="If collateral falls below this level you should prompt the borrower to top up."
          />
          <StatCard
            label="Mission impact"
            value="Empowering the Unbanked"
            helper="Your app can extend fair, permissionless credit rails to local communities who lack traditional banking."
          />
        </div>
      </section>

      <section style={{ marginTop: 48 }}>
        <h2 className="section-title">Implementation notes</h2>
        <div className="info-grid">
          <StatCard
            label="Borrow with fee"
            value={<code>lend_borrowNHBWithFee</code>}
            helper="Send the developer's fee recipient address along with the borrow transaction."
          />
          <StatCard
            label="Collateral management"
            value={<code>lend_depositZNHB</code>}
            helper="Track the borrower's ZNHB collateralization level to protect against liquidation."
          />
          <StatCard
            label="Risk monitoring"
            value={<code>lend_getHealthFactor</code>}
            helper="Surface proactive alerts and educational content when the health factor drops."
          />
        </div>
      </section>
    </>
  );
}
