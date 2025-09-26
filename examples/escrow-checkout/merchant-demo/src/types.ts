export type EscrowSessionStatus =
  | 'AWAITING_FUNDS'
  | 'FUNDED'
  | 'DELIVERED'
  | 'RELEASED'
  | 'CANCELLED'
  | 'EXPIRED';

export interface MoneyAmount {
  currency: string;
  value: string;
}

export type EscrowSessionEvent =
  | {
      type: 'status';
      status: EscrowSessionStatus;
      at: string;
      note?: string;
    }
  | {
      type: 'milestone';
      at: string;
      label: string;
      amount?: MoneyAmount;
      note?: string;
    };

export interface EscrowSession {
  sessionId: string;
  escrowId: string;
  depositAddress: string;
  paymentUri: string;
  amount: MoneyAmount;
  status: EscrowSessionStatus;
  expiresAt?: string;
  customer?: {
    walletAddress?: string;
  };
  milestoneMode?: boolean;
  history?: EscrowSessionEvent[];
  milestones?: Array<{
    id: string;
    title: string;
    status: string;
    targetAmount?: MoneyAmount;
    releasedAmount?: MoneyAmount;
    completedAt?: string;
  }>;
}

export interface EscrowDemoConfig {
  apiBase: string;
  apiKey: string;
  apiSecret: string;
  webhookSecret: string;
  walletPrivateKey: string;
  port: number;
}
