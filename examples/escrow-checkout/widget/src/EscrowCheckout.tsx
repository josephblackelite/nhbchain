import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { QRCodeSVG } from 'qrcode.react';
import clsx from 'clsx';

const STYLE_TAG_ID = 'nhb-escrow-checkout-styles';

const STYLE_SHEET = `
.nhb-escrow-checkout {
  border: 1px solid #e0e4f0;
  border-radius: 12px;
  padding: 24px;
  max-width: 420px;
  font-family: 'Inter', system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
  background: #ffffff;
  box-shadow: 0 8px 24px rgba(12, 22, 44, 0.08);
}

.nhb-escrow-checkout header {
  margin-bottom: 16px;
}

.nhb-escrow-checkout h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.nhb-escrow-status {
  margin: 8px 0 0 0;
  color: #445577;
}

.nhb-escrow-amount {
  margin: 8px 0 0 0;
  font-size: 18px;
  color: #1a1a1f;
}

.nhb-escrow-error {
  background: #ffefef;
  border: 1px solid #ffb0b0;
  color: #7a0010;
  padding: 12px;
  border-radius: 8px;
  margin-bottom: 16px;
}

.nhb-escrow-placeholder {
  padding: 24px;
  text-align: center;
  color: #6b7280;
  border: 1px dashed #d1d5db;
  border-radius: 8px;
}

.nhb-escrow-session {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.nhb-escrow-qr {
  align-self: center;
  text-align: center;
}

.nhb-escrow-qr p {
  margin-top: 8px;
  font-size: 14px;
  color: #4b5563;
}

.nhb-escrow-details {
  display: grid;
  gap: 12px;
}

.nhb-escrow-details dt {
  font-weight: 600;
  color: #1f2937;
}

.nhb-escrow-details dd {
  margin: 4px 0 0 0;
  color: #4b5563;
  word-break: break-all;
}

.nhb-escrow-address {
  font-family: 'Roboto Mono', 'Fira Mono', monospace;
}

.nhb-escrow-actions {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.nhb-escrow-actions button {
  background: linear-gradient(90deg, #4353ff, #6b00f5);
  color: white;
  border: none;
  padding: 12px;
  border-radius: 8px;
  cursor: pointer;
  font-size: 16px;
  font-weight: 600;
  transition: transform 0.1s ease, opacity 0.1s ease;
}

.nhb-escrow-actions button[disabled] {
  opacity: 0.5;
  cursor: not-allowed;
}

.nhb-escrow-actions button:not([disabled]):active {
  transform: scale(0.98);
}

.nhb-escrow-hint {
  font-size: 13px;
  color: #6b7280;
}

.nhb-escrow-history {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.nhb-escrow-history-status {
  font-weight: 600;
  color: #111827;
  margin-right: 8px;
}

.nhb-escrow-history-note {
  margin: 4px 0 0 0;
  color: #4b5563;
  font-size: 14px;
}
`;

function ensureStylesInjected() {
  if (typeof document === 'undefined') return;
  if (document.getElementById(STYLE_TAG_ID)) return;
  const style = document.createElement('style');
  style.id = STYLE_TAG_ID;
  style.textContent = STYLE_SHEET;
  document.head.appendChild(style);
}

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
  actions?: {
    deliverUrl?: string;
    releaseUrl?: string;
  };
}

export interface EscrowCheckoutProps {
  /**
   * Merchant demo server base URL. Must expose the REST surface documented in
   * docs/examples/escrow-checkout.md.
   */
  merchantBaseUrl: string;
  /** Merchant order identifier. Used for idempotent checkout session creation. */
  orderId: string;
  /** Optional wallet address for the buyer. */
  customerWalletAddress?: string;
  /**
   * Optional amount information that will be displayed while the widget waits
   * for the merchant server to return the session. When the session resolves
   * this value is replaced with the authoritative amount from the API.
   */
  expectedAmount?: MoneyAmount;
  /**
   * Milliseconds between status polling calls. Defaults to 5 seconds.
   */
  pollIntervalMs?: number;
  /**
   * Automatically create the checkout session on mount. Disable this if the
   * host application wants to trigger creation manually through the returned
   * controller.
   */
  autoCreate?: boolean;
  /**
   * Callback invoked whenever the escrow session status changes.
   */
  onStatusChange?: (status: EscrowSessionStatus) => void;
  className?: string;
  /**
   * Custom renderer for the session timeline (history). Receives the raw
   * history entries so the host app can display its own view.
   */
  renderHistory?: (history: NonNullable<EscrowSession['history']>) => React.ReactNode;
  /**
   * Optionally surface the controller to the host app. Useful when
   * autoCreate=false and the parent wants manual control.
   */
  onController?: (controller: EscrowCheckoutController) => void;
}

export interface EscrowCheckoutController {
  createSession: () => Promise<void>;
  refresh: () => Promise<void>;
  markDelivered: () => Promise<void>;
  release: () => Promise<void>;
}

interface RestError {
  status: number;
  message: string;
}

const DEFAULT_POLL_INTERVAL = 5_000;

async function requestJSON<T>(input: RequestInfo, init?: RequestInit): Promise<T> {
  const res = await fetch(input, {
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers ?? {})
    },
    ...init
  });

  if (!res.ok) {
    let message = res.statusText;
    try {
      const data = (await res.json()) as { error?: string; message?: string };
      message = data.error || data.message || message;
    } catch (err) {
      // Ignore JSON parse failures; we already have the status text.
    }

    const error: RestError = { status: res.status, message };
    throw error;
  }

  return (await res.json()) as T;
}

function mergeHistory(a?: EscrowSession['history'], b?: EscrowSession['history']) {
  if (!a) return b;
  if (!b) return a;
  const map = new Map<string, { status: EscrowSessionStatus; at: string; note?: string }>();
  [...a, ...b].forEach((entry) => {
    map.set(`${entry.status}-${entry.at}`, entry);
  });
  return Array.from(map.values()).sort((x, y) => x.at.localeCompare(y.at));
}

function SessionHistory({ history }: { history: EscrowSession['history'] }) {
  if (!history?.length) return null;
  return (
    <ol className="nhb-escrow-history">
      {history.map((entry) => (
        <li key={`${entry.status}-${entry.at}`}>
          <span className="nhb-escrow-history-status">{entry.status.replace('_', ' ')}</span>
          <time dateTime={entry.at}>{new Date(entry.at).toLocaleString()}</time>
          {entry.note && <p className="nhb-escrow-history-note">{entry.note}</p>}
        </li>
      ))}
    </ol>
  );
}

export const EscrowCheckout: React.FC<EscrowCheckoutProps> = ({
  merchantBaseUrl,
  orderId,
  customerWalletAddress,
  expectedAmount,
  pollIntervalMs = DEFAULT_POLL_INTERVAL,
  autoCreate = true,
  onStatusChange,
  className,
  renderHistory,
  onController
}) => {
  useEffect(() => {
    ensureStylesInjected();
  }, []);

  const [session, setSession] = useState<EscrowSession | null>(null);
  const [isLoading, setIsLoading] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);
  const [isDelivering, setIsDelivering] = useState<boolean>(false);
  const [isReleasing, setIsReleasing] = useState<boolean>(false);
  const statusRef = useRef<EscrowSessionStatus | null>(null);
  const pollerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const baseUrl = merchantBaseUrl.replace(/\/$/, '');

  const updateSession = useCallback(
    (next: EscrowSession) => {
      setSession((prev) => {
        const history = mergeHistory(prev?.history, next.history);
        const merged = { ...(prev ?? {}), ...next, history } as EscrowSession;
        return merged;
      });
      if (statusRef.current !== next.status) {
        statusRef.current = next.status;
        onStatusChange?.(next.status);
      }
    },
    [onStatusChange]
  );

  const fetchSession = useCallback(async () => {
    if (!session?.sessionId) return;
    try {
      const data = await requestJSON<EscrowSession>(`${baseUrl}/api/checkout/session/${session.sessionId}`);
      updateSession(data);
    } catch (err) {
      console.warn('Failed to refresh escrow session', err);
    }
  }, [baseUrl, session?.sessionId, updateSession]);

  const createSession = useCallback(async () => {
    if (isLoading) return;
    setIsLoading(true);
    setError(null);
    try {
      const data = await requestJSON<EscrowSession>(`${baseUrl}/api/checkout/session`, {
        method: 'POST',
        body: JSON.stringify({ orderId, customerWalletAddress })
      });
      statusRef.current = data.status;
      setSession(data);
      onStatusChange?.(data.status);
    } catch (err) {
      const restErr = err as RestError;
      setError(restErr.message || 'Unable to start escrow checkout');
    } finally {
      setIsLoading(false);
    }
  }, [baseUrl, customerWalletAddress, isLoading, onStatusChange, orderId]);

  const markDelivered = useCallback(async () => {
    if (!session?.escrowId) return;
    setIsDelivering(true);
    setError(null);
    try {
      const data = await requestJSON<EscrowSession>(`${baseUrl}/api/escrow/${session.escrowId}/deliver`, {
        method: 'POST'
      });
      updateSession(data);
    } catch (err) {
      const restErr = err as RestError;
      setError(restErr.message || 'Failed to mark delivery');
    } finally {
      setIsDelivering(false);
    }
  }, [baseUrl, session?.escrowId, updateSession]);

  const release = useCallback(async () => {
    if (!session?.escrowId) return;
    setIsReleasing(true);
    setError(null);
    try {
      const data = await requestJSON<EscrowSession>(`${baseUrl}/api/escrow/${session.escrowId}/release`, {
        method: 'POST'
      });
      updateSession(data);
    } catch (err) {
      const restErr = err as RestError;
      setError(restErr.message || 'Failed to release funds');
    } finally {
      setIsReleasing(false);
    }
  }, [baseUrl, session?.escrowId, updateSession]);

  useEffect(() => {
    const controller: EscrowCheckoutController = {
      createSession,
      refresh: fetchSession,
      markDelivered,
      release
    };
    onController?.(controller);
  }, [createSession, fetchSession, markDelivered, onController, release]);

  useEffect(() => {
    if (!autoCreate) return;
    if (session) return;
    createSession();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoCreate, session]);

  useEffect(() => {
    if (!session?.sessionId) return;
    if (pollerRef.current) {
      clearInterval(pollerRef.current);
    }
    pollerRef.current = setInterval(fetchSession, pollIntervalMs);
    return () => {
      if (pollerRef.current) {
        clearInterval(pollerRef.current);
        pollerRef.current = null;
      }
    };
  }, [fetchSession, pollIntervalMs, session?.sessionId]);

  const amount = session?.amount ?? expectedAmount;
  const paymentUri = session?.paymentUri;
  const status = session?.status;
  const isComplete = status === 'RELEASED' || status === 'CANCELLED';
  const awaitingFunds = status === 'AWAITING_FUNDS';
  const canDeliver = status === 'FUNDED';
  const canRelease = status === 'DELIVERED';

  const statusLabel = useMemo(() => {
    switch (status) {
      case 'AWAITING_FUNDS':
        return 'Waiting for buyer to fund escrow';
      case 'FUNDED':
        return 'Funds are in escrow';
      case 'DELIVERED':
        return 'Waiting for release';
      case 'RELEASED':
        return 'Escrow released';
      case 'CANCELLED':
        return 'Escrow cancelled';
      case 'EXPIRED':
        return 'Escrow expired';
      default:
        return 'Starting checkout';
    }
  }, [status]);

  return (
    <section className={clsx('nhb-escrow-checkout', className)}>
      <header>
        <h2>Escrow checkout</h2>
        <p className="nhb-escrow-status">{statusLabel}</p>
        {amount && (
          <p className="nhb-escrow-amount">
            <strong>
              {amount.value} {amount.currency}
            </strong>
          </p>
        )}
      </header>

      {error && <div className="nhb-escrow-error">{error}</div>}

      {!session && (
        <div className="nhb-escrow-placeholder">
          {isLoading ? 'Generating escrow session…' : 'Preparing checkout session…'}
        </div>
      )}

      {session && (
        <div className="nhb-escrow-session">
          {awaitingFunds && paymentUri && (
            <div className="nhb-escrow-qr">
              <QRCodeSVG value={paymentUri} size={180} />
              <p>Scan to fund escrow from any NHB-compatible wallet.</p>
            </div>
          )}

          <dl className="nhb-escrow-details">
            <div>
              <dt>Escrow ID</dt>
              <dd>{session.escrowId}</dd>
            </div>
            <div>
              <dt>Deposit address</dt>
              <dd className="nhb-escrow-address">{session.depositAddress}</dd>
            </div>
            {session.customer?.walletAddress && (
              <div>
                <dt>Buyer</dt>
                <dd>{session.customer.walletAddress}</dd>
              </div>
            )}
            {session.expiresAt && (
              <div>
                <dt>Expires</dt>
                <dd>
                  <time dateTime={session.expiresAt}>{new Date(session.expiresAt).toLocaleString()}</time>
                </dd>
              </div>
            )}
          </dl>

          <div className="nhb-escrow-actions">
            <button type="button" disabled={!canDeliver || isDelivering} onClick={markDelivered}>
              {isDelivering ? 'Notifying escrow…' : 'Mark as delivered'}
            </button>
            <button type="button" disabled={!canRelease || isReleasing} onClick={release}>
              {isReleasing ? 'Releasing…' : 'Release funds'}
            </button>
          </div>

          {!isComplete && (
            <p className="nhb-escrow-hint">
              The widget polls {Math.round(pollIntervalMs / 1000)}s for escrow status updates and triggers webhooks on the
              merchant demo server.
            </p>
          )}

          {session.history && (renderHistory ? renderHistory(session.history) : <SessionHistory history={session.history} />)}
        </div>
      )}
    </section>
  );
};

export default EscrowCheckout;
