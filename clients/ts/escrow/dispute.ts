export interface EscrowDisputeClientOptions {
  /** Base URL of the NHB JSON-RPC endpoint (e.g. https://gateway/v1/consensus). */
  baseUrl: string;
  /** Bearer token used for authenticated RPCs such as escrow_dispute. */
  authToken: string;
  /** Optional fetch implementation (defaults to globalThis.fetch). */
  fetchImpl?: typeof fetch;
}

interface RpcError {
  code: number;
  message: string;
  data?: unknown;
}

interface RpcResponse<T> {
  result?: T;
  error?: RpcError;
}

interface EscrowState {
  id: string;
  payer: string;
  payee: string;
  status: string;
}

function assertFetch(fetchImpl: typeof fetch | undefined): asserts fetchImpl {
  if (typeof fetchImpl !== 'function') {
    throw new Error('A fetch implementation must be provided.');
  }
}

function normaliseBaseUrl(url: string): string {
  const trimmed = url?.trim();
  if (!trimmed) {
    throw new Error('baseUrl is required');
  }
  return trimmed;
}

export class EscrowDisputeClient {
  private readonly baseUrl: string;

  private readonly authToken: string;

  private readonly fetchImpl: typeof fetch;

  constructor(options: EscrowDisputeClientOptions) {
    this.baseUrl = normaliseBaseUrl(options.baseUrl);
    const token = options.authToken?.trim();
    if (!token) {
      throw new Error('authToken is required for escrow disputes');
    }
    this.authToken = token;
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch;
    assertFetch(this.fetchImpl);
  }

  /**
   * Opens a dispute on the specified escrow.
   * Automatically resolves the recorded payer address and can attach an optional reason.
   */
  async dispute(escrowId: string, reason?: string): Promise<string> {
    const trimmedId = escrowId?.trim();
    if (!trimmedId) {
      throw new Error('escrowId is required');
    }
    const escrow = await this.fetchEscrow(trimmedId);
    if (!escrow?.payer?.trim()) {
      throw new Error('Escrow record is missing payer information');
    }
    const payload: Record<string, string> = {
      id: escrow.id,
      caller: escrow.payer,
    };
    const trimmedReason = reason?.trim();
    if (trimmedReason) {
      payload.reason = trimmedReason;
    }
    return this.rpc<string>('escrow_dispute', [payload], true);
  }

  private async fetchEscrow(escrowId: string): Promise<EscrowState> {
    const result = await this.rpc<EscrowState>('escrow_get', [{ id: escrowId }], false);
    if (!result || result.id !== escrowId) {
      throw new Error('Escrow lookup returned an unexpected record');
    }
    return result;
  }

  private async rpc<T>(method: string, params: unknown[], requireAuth: boolean): Promise<T> {
    const body = JSON.stringify({ jsonrpc: '2.0', id: 1, method, params });
    const headers: Record<string, string> = { 'content-type': 'application/json' };
    if (requireAuth) {
      headers.authorization = `Bearer ${this.authToken}`;
    }
    const response = await this.fetchImpl(this.baseUrl, {
      method: 'POST',
      headers,
      body,
    });
    if (!response.ok) {
      throw new Error(`RPC request failed with status ${response.status}`);
    }
    const payload: RpcResponse<T> = await response.json();
    if (payload.error) {
      throw new Error(payload.error.message || 'RPC returned an error');
    }
    if (payload.result === undefined) {
      throw new Error('RPC response missing result');
    }
    return payload.result;
  }
}

export default EscrowDisputeClient;
