import { FormEvent, useState } from 'react';
import { formatNumber } from '../lib/utils';

type Props = {
  collateral: number;
  debt: number;
  healthFactor: number;
  onDeposit(amount: number): void;
  onBorrow(amount: number, feeRecipient: string): void;
  onRepay(amount: number): void;
};

export function BorrowForm({ collateral, debt, healthFactor, onDeposit, onBorrow, onRepay }: Props) {
  const [znHBAmount, setZnHBAmount] = useState('');
  const [nhbAmount, setNhbAmount] = useState('');
  const [feeRecipient, setFeeRecipient] = useState('');

  const handleDeposit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const parsed = Number(znHBAmount);
    if (!Number.isFinite(parsed) || parsed <= 0) return;
    onDeposit(parsed);
    setZnHBAmount('');
  };

  const handleBorrow = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const parsed = Number(nhbAmount);
    if (!Number.isFinite(parsed) || parsed <= 0) return;
    onBorrow(parsed, feeRecipient);
    setNhbAmount('');
  };

  const handleRepay = () => {
    const parsed = Number(nhbAmount);
    if (!Number.isFinite(parsed) || parsed <= 0) return;
    onRepay(parsed);
    setNhbAmount('');
  };

  return (
    <div className="card-grid">
      <form className="form-card" onSubmit={handleDeposit}>
        <div className="page-heading">
          <span>Collateral</span>
          <h1>Deposit ZNHB to unlock borrowing</h1>
        </div>
        <div className="form-section">
          <label htmlFor="deposit">Deposit amount (ZNHB)</label>
          <input
            id="deposit"
            placeholder="0.00"
            type="number"
            min="0"
            step="0.01"
            value={znHBAmount}
            onChange={(event) => setZnHBAmount(event.target.value)}
          />
          <p className="helper-text">
            Current collateral: <strong>{formatNumber(collateral)} ZNHB</strong>
          </p>
        </div>
        <button className="button-primary" type="submit">
          Deposit Collateral
        </button>
      </form>

      <form className="form-card" onSubmit={handleBorrow}>
        <div className="page-heading">
          <span>Borrow</span>
          <h1>Access NHB liquidity</h1>
        </div>
        <div className="form-section">
          <label htmlFor="borrow">Borrow amount (NHB)</label>
          <input
            id="borrow"
            placeholder="0.00"
            type="number"
            min="0"
            step="0.01"
            value={nhbAmount}
            onChange={(event) => setNhbAmount(event.target.value)}
          />
        </div>
        <div className="form-section">
          <label htmlFor="feeRecipient">Developer fee recipient</label>
          <input
            id="feeRecipient"
            placeholder="0x..."
            value={feeRecipient}
            onChange={(event) => setFeeRecipient(event.target.value)}
          />
          <p className="helper-text">
            Demonstrates how the `lend_borrowNHBWithFee` endpoint shares revenue with your application.
          </p>
        </div>
        <div className="stat-row">
          <span>Health factor</span>
          <strong>{healthFactor.toFixed(2)}</strong>
        </div>
        <div className="stat-row">
          <span>Outstanding debt</span>
          <strong>{formatNumber(debt)} NHB</strong>
        </div>
        <div className="inline-actions">
          <button className="button-primary" type="submit">
            Borrow with Fee
          </button>
          <button className="button-secondary" type="button" onClick={handleRepay}>
            Repay Loan
          </button>
        </div>
      </form>
    </div>
  );
}
