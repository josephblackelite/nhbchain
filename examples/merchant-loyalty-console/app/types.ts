export interface BusinessResult {
  id: string;
  owner: string;
  name: string;
  paymaster: string;
  merchants: string[];
}

export interface ProgramResult {
  id: string;
  owner: string;
  pool: string;
  tokenSymbol: string;
  accrualBps: number;
  minSpendWei: string;
  capPerTx: string;
  dailyCapUser: string;
  startTime: number;
  endTime: number;
  active: boolean;
}

export interface ProgramStats {
  rewardsPaid: string;
  txCount: string;
  capUsage?: string;
  skips?: string;
}
