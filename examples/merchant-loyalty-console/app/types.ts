export interface BusinessResult {
  id: string;
  owner: string;
  name: string;
  paymaster: string;
  merchants: string[];
  creatorRewardsPool?: string;
  fanRewardsEnabled?: boolean;
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

export interface FanRewardsConfig {
  pool: string;
  creatorShareBps: number;
  fanShareBps: number;
  treasuryShareBps: number;
  enabled: boolean;
}

export interface FanRewardsStats {
  distributed: string;
  pending: string;
  supporters: number;
  lastPayoutAt?: number;
}
