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
  history?: Array<{
    status: EscrowSessionStatus;
    at: string;
    note?: string;
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
