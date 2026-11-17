export interface EscrowDisputeClientOptions {
  /** Base URL of the NHB JSON-RPC endpoint (e.g. https://gateway/v1/consensus). */
  baseUrl: string;
  /** Bearer token the node's authenticated RPCs expect. */
  authToken: string;
  /** Optional fetch implementation (defaults to globalThis.fetch). */
  fetchImpl?: typeof fetch;
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

/** The message dispute() fails with; the node refuses the RPC it used to call. */
export const ESCROW_DISPUTE_RETIRED =
  'escrow_dispute is retired: the node answers it with HTTP 410 and never opens a dispute. ' +
  'Open a dispute by signing a TxTypeDisputeEscrow (0x0A) transaction whose data is the escrow id and ' +
  'sending it with nhb_sendTransaction.';

export class EscrowDisputeClient {
  constructor(options: EscrowDisputeClientOptions) {
    normaliseBaseUrl(options.baseUrl);
    if (!options.authToken?.trim()) {
      throw new Error('authToken is required for escrow disputes');
    }
    assertFetch(options.fetchImpl ?? globalThis.fetch);
  }

  /**
   * Opening a dispute used to call the escrow_dispute RPC. The node retired that method (it writes state
   * outside block execution), so every call came back as HTTP 410. This now fails at once, without
   * contacting the node, and says what to do instead.
   */
  async dispute(escrowId: string, _reason?: string): Promise<string> {
    if (!escrowId?.trim()) {
      throw new Error('escrowId is required');
    }
    throw new Error(ESCROW_DISPUTE_RETIRED);
  }
}

export default EscrowDisputeClient;
