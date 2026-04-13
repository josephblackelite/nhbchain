import { FormEvent, useState } from 'react';
import { formatNumber } from '../lib/utils';

type Props = {
  availableBalance: number;
  suppliedBalance: number;
  onSupply(amount: number): void;
  onWithdraw(amount: number): void;
};

export function EarnForm({ availableBalance, suppliedBalance, onSupply, onWithdraw }: Props) {
  const [amount, setAmount] = useState('');

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const parsed = Number(amount);
    if (!Number.isFinite(parsed) || parsed <= 0) return;

    const nativeEvent = event.nativeEvent as SubmitEvent;
    const action = (nativeEvent.submitter as HTMLButtonElement | null)?.dataset.action;

    if (action === 'supply') {
      onSupply(parsed);
    } else {
      onWithdraw(parsed);
    }
    setAmount('');
  };

  return (
    <form className="form-card" onSubmit={handleSubmit}>
      <div className="page-heading">
        <span>Earn Yield</span>
        <h1>Supply NHB and accrue returns</h1>
      </div>

      <div className="form-section">
        <label htmlFor="amount">Amount (NHB)</label>
        <input
          id="amount"
          placeholder="0.00"
          value={amount}
          onChange={(event) => setAmount(event.target.value)}
          type="number"
          min="0"
          step="0.01"
        />
        <p className="helper-text">
          Available balance: <strong>{formatNumber(availableBalance)} NHB</strong>
        </p>
      </div>

      <div className="form-section">
        <p className="helper-text">
          Total supplied to the pool: <strong>{formatNumber(suppliedBalance)} NHB</strong>
        </p>
      </div>

      <div className="inline-actions">
        <button className="button-primary" type="submit" data-action="supply">
          Supply
        </button>
        <button className="button-secondary" type="submit" data-action="withdraw">
          Withdraw
        </button>
      </div>
    </form>
  );
}
