import { useState } from 'react';
import { clamp } from './utils';

const MAX_UTILIZATION = 0.75;
const LIQUIDATION_THRESHOLD = 0.85;

export function useEarnState() {
  const [availableBalance, setAvailableBalance] = useState(5400);
  const [suppliedBalance, setSuppliedBalance] = useState(2600);

  const supply = (amount: number) => {
    setAvailableBalance((prev) => clamp(prev - amount, 0, Number.MAX_SAFE_INTEGER));
    setSuppliedBalance((prev) => prev + amount);
  };

  const withdraw = (amount: number) => {
    setSuppliedBalance((prev) => clamp(prev - amount, 0, Number.MAX_SAFE_INTEGER));
    setAvailableBalance((prev) => prev + amount);
  };

  return { availableBalance, suppliedBalance, supply, withdraw };
}

export function useBorrowState() {
  const [collateral, setCollateral] = useState(1200);
  const [debt, setDebt] = useState(420);

  const deposit = (amount: number) => {
    setCollateral((prev) => prev + amount);
  };

  const borrow = (amount: number) => {
    setDebt((prev) => prev + amount);
  };

  const repay = (amount: number) => {
    setDebt((prev) => clamp(prev - amount, 0, Number.MAX_SAFE_INTEGER));
  };

  const healthFactor = calculateHealthFactor(collateral, debt);

  return { collateral, debt, deposit, borrow, repay, healthFactor };
}

function calculateHealthFactor(collateral: number, debt: number) {
  if (debt === 0) return 5;
  const utilization = debt / Math.max(collateral, 1);
  const ratio = 1 - utilization / MAX_UTILIZATION;
  const score = 2 + ratio * 2;
  return clamp(score, 0.1, 5);
}

export function getBorrowInsights(collateral: number) {
  const maxBorrow = collateral * MAX_UTILIZATION;
  const liquidationPrice = collateral * LIQUIDATION_THRESHOLD;
  return {
    maxBorrow,
    liquidationPrice
  };
}
