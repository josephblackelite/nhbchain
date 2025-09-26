import axios, { AxiosInstance } from 'axios';
import crypto from 'node:crypto';
import type { EscrowDemoConfig, EscrowSession, EscrowSessionStatus, MoneyAmount } from './types.js';
import { WalletSigner } from './signing.js';

interface ApiAmount {
  currency: string;
  value: string;
}

interface ApiMilestone {
  id: string;
  title: string;
  status: string;
  target_amount?: ApiAmount;
  released_amount?: ApiAmount;
  completed_at?: string;
}

interface ApiHistoryBase {
  at: string;
  note?: string;
  type?: string;
}

interface ApiStatusHistory extends ApiHistoryBase {
  status: string;
}

interface ApiMilestoneHistory extends ApiHistoryBase {
  milestone?: {
    title?: string;
    amount?: ApiAmount;
  };
  label?: string;
  amount?: ApiAmount;
}

type ApiHistoryItem = ApiStatusHistory | ApiMilestoneHistory;

interface ApiSession {
  session_id: string;
  escrow_id: string;
  deposit_address: string;
  payment_uri: string;
  status: string;
  expires_at?: string;
  amount: ApiAmount;
  customer?: {
    wallet_address?: string;
  };
  milestone_mode?: boolean;
  history?: ApiHistoryItem[];
  milestones?: ApiMilestone[];
}

interface ApiResponse<T> {
  data: T;
}

function normaliseStatus(status: string): EscrowSessionStatus {
  const normalised = status.toUpperCase() as EscrowSessionStatus;
  return normalised;
}

function normaliseAmount(amount: ApiAmount): MoneyAmount {
  return {
    currency: amount.currency,
    value: amount.value
  };
}

function transformHistory(item: ApiHistoryItem): EscrowSession['history'][number] {
  if ((item as ApiMilestoneHistory).milestone || item.type === 'milestone') {
    const milestone = (item as ApiMilestoneHistory).milestone;
    const amount = (item as ApiMilestoneHistory).amount || milestone?.amount;
    return {
      type: 'milestone',
      at: item.at,
      label: (item as ApiMilestoneHistory).label || milestone?.title || 'Milestone event',
      amount: amount ? normalizeAmount(amount) : undefined,
      note: item.note,
    };
  }
  const statusItem = item as ApiStatusHistory;
  return {
    type: 'status',
    status: normaliseStatus(statusItem.status),
    at: statusItem.at,
    note: item.note,
  };
}

function transformMilestone(milestone: ApiMilestone) {
  return {
    id: milestone.id,
    title: milestone.title,
    status: milestone.status,
    targetAmount: milestone.target_amount ? normalizeAmount(milestone.target_amount) : undefined,
    releasedAmount: milestone.released_amount ? normalizeAmount(milestone.released_amount) : undefined,
    completedAt: milestone.completed_at,
  };
}

function transformSession(session: ApiSession): EscrowSession {
  return {
    sessionId: session.session_id,
    escrowId: session.escrow_id,
    depositAddress: session.deposit_address,
    paymentUri: session.payment_uri,
    amount: normaliseAmount(session.amount),
    status: normaliseStatus(session.status),
    expiresAt: session.expires_at,
    customer: session.customer?.wallet_address ? { walletAddress: session.customer.wallet_address } : undefined,
    milestoneMode: session.milestone_mode ?? false,
    history: session.history?.map((item) => transformHistory(item)),
    milestones: session.milestones?.map((milestone) => transformMilestone(milestone))
  };
}

export class EscrowClient {
  private readonly http: AxiosInstance;
  private readonly signer: WalletSigner;

  constructor(private readonly config: EscrowDemoConfig) {
    this.http = axios.create({
      baseURL: config.apiBase,
      timeout: 15_000
    });
    this.signer = new WalletSigner(config.walletPrivateKey);
  }

  private buildSignature(method: string, path: string, payload: string) {
    const timestamp = new Date().toISOString();
    const preimage = `${timestamp}.${method.toUpperCase()}.${path}.${payload}`;
    const signature = crypto.createHmac('sha256', this.config.apiSecret).update(preimage).digest('hex');
    return { timestamp, signature };
  }

  private async request<T>(
    method: 'GET' | 'POST',
    path: string,
    body?: Record<string, unknown>,
    options?: { idempotencyKey?: string }
  ): Promise<T> {
    const payload = body ? JSON.stringify(body) : '';
    const { timestamp, signature } = this.buildSignature(method, path, payload);
    const headers: Record<string, string> = {
      'X-NHB-API-Key': this.config.apiKey,
      'X-NHB-Timestamp': timestamp,
      'X-NHB-Signature': signature,
      'Content-Type': 'application/json'
    };
    if (options?.idempotencyKey) {
      headers['Idempotency-Key'] = options.idempotencyKey;
    }

    const response = await this.http.request<ApiResponse<T>>({
      method,
      url: path,
      data: body,
      headers
    });

    return response.data.data;
  }

  async createCheckoutSession(
    orderId: string,
    walletAddress?: string,
    options?: { milestoneMode?: boolean }
  ): Promise<EscrowSession> {
    const milestoneMode = options?.milestoneMode ?? false;
    const session = await this.request<ApiSession>(
      'POST',
      '/v1/escrow/checkout/sessions',
      {
        order_id: orderId,
        customer_wallet_address: walletAddress,
        milestone_mode: milestoneMode
      },
      { idempotencyKey: milestoneMode ? `${orderId}-milestone` : orderId }
    );
    return transformSession(session);
  }

  async getCheckoutSession(sessionId: string): Promise<EscrowSession> {
    const session = await this.request<ApiSession>('GET', `/v1/escrow/checkout/sessions/${sessionId}`);
    return transformSession(session);
  }

  async markDelivered(escrowId: string): Promise<EscrowSession> {
    const session = await this.request<ApiSession>('POST', `/v1/escrow/escrows/${escrowId}/deliver`);
    return transformSession(session);
  }

  async releaseEscrow(escrowId: string): Promise<EscrowSession> {
    const signedAt = new Date().toISOString();
    const message = `${escrowId}.${signedAt}`;
    const signature = this.signer.sign(message);
    const walletAddress = this.signer.getPublicKey();
    const session = await this.request<ApiSession>('POST', `/v1/escrow/escrows/${escrowId}/release`, {
      wallet_address: walletAddress,
      signed_at: signedAt,
      signature
    });
    return transformSession(session);
  }
}
