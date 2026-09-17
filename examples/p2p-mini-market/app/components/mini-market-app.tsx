'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { PayIntentCard } from './pay-intent-card';
import { deriveAddressFromPrivateKey, randomPrivateKey } from '../lib/wallet';
import { formatAmount, formatStatus, formatTimestamp, toBaseUnits } from '../lib/format';

interface BalanceResponse {
  address: string;
  balanceNHB: string;
  balanceZNHB: string;
  lockedZNHB: string;
  stake: string;
  nonce: number;
}

interface RpcPayIntent {
  to: string;
  token: string;
  amount: string;
  memo?: string;
}

interface TradeCreationResponse {
  tradeId: string;
  escrowBaseId: string;
  escrowQuoteId: string;
  payIntents: Record<string, RpcPayIntent>;
}

interface TradeSnapshot {
  id: string;
  offerId: string;
  buyer: string;
  seller: string;
  quoteToken: string;
  quoteAmount: string;
  escrowQuoteId: string;
  baseToken: string;
  baseAmount: string;
  escrowBaseId: string;
  deadline: number;
  createdAt: number;
  status: string;
}

interface EscrowSnapshot {
  id: string;
  payer: string;
  payee: string;
  token: string;
  amount: string;
  status: string;
  deadline: number;
  createdAt: number;
}

interface OfferRecord {
  id: string;
  seller: string;
  direction: 'SELL_NHB' | 'SELL_ZNHB';
  baseToken: 'NHB' | 'ZNHB';
  quoteToken: 'NHB' | 'ZNHB';
  baseAmount: string; // base units
  quoteAmount: string; // base units
  baseDisplay: string;
  quoteDisplay: string;
  deadlineHours: number;
  terms?: string;
  createdAt: number;
}

interface TradeRecord {
  tradeId: string;
  offerId: string;
  buyer: string;
  seller: string;
  baseToken: string;
  quoteToken: string;
  baseAmount: string;
  quoteAmount: string;
  escrowBaseId: string;
  escrowQuoteId: string;
  payIntents: Record<string, RpcPayIntent>;
  createdAt: number;
  deadline: number;
  status?: string;
  baseEscrow?: EscrowSnapshot;
  quoteEscrow?: EscrowSnapshot;
  lastUpdated?: number;
  error?: string | null;
}

const storageOffersKey = 'nhb:p2p-mini-market:offers';
const storageTradesKey = 'nhb:p2p-mini-market:trades';

function persist<T>(key: string, value: T) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch (error) {
    console.warn('Failed to persist state', error);
  }
}

function restore<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return fallback;
    return JSON.parse(raw) as T;
  } catch (error) {
    console.warn('Failed to restore state', error);
    return fallback;
  }
}

async function fetchJson<T>(input: RequestInfo, init?: RequestInit): Promise<T> {
  const response = await fetch(input, init);
  const payload = await response.json();
  if (!response.ok) {
    const message = payload?.error ? JSON.stringify(payload.error) : response.statusText;
    throw new Error(message || 'Request failed');
  }
  return payload as T;
}

export default function MiniMarketApp() {
  const [sellerKey, setSellerKey] = useState('');
  const [sellerAddress, setSellerAddress] = useState('');
  const [sellerAccount, setSellerAccount] = useState<BalanceResponse | null>(null);
  const [sellerError, setSellerError] = useState<string | null>(null);

  const [buyerKey, setBuyerKey] = useState('');
  const [buyerAddress, setBuyerAddress] = useState('');
  const [buyerAccount, setBuyerAccount] = useState<BalanceResponse | null>(null);
  const [buyerError, setBuyerError] = useState<string | null>(null);

  const [offers, setOffers] = useState<OfferRecord[]>([]);
  const [trades, setTrades] = useState<TradeRecord[]>([]);

  const [offerDirection, setOfferDirection] = useState<'SELL_NHB' | 'SELL_ZNHB'>('SELL_NHB');
  const [baseAmountInput, setBaseAmountInput] = useState('10.0');
  const [quoteAmountInput, setQuoteAmountInput] = useState('10.0');
  const [deadlineHours, setDeadlineHours] = useState(6);
  const [offerTerms, setOfferTerms] = useState('');
  const [offerError, setOfferError] = useState<string | null>(null);

  const [acceptingOfferId, setAcceptingOfferId] = useState<string | null>(null);
  const [acceptError, setAcceptError] = useState<string | null>(null);

  const [fundingEscrow, setFundingEscrow] = useState<string | null>(null);
  const [fundError, setFundError] = useState<string | null>(null);

  const [settleError, setSettleError] = useState<string | null>(null);
  const [settlingTrade, setSettlingTrade] = useState<string | null>(null);

  const [disputeError, setDisputeError] = useState<string | null>(null);
  const [resolveError, setResolveError] = useState<string | null>(null);
  const [resolutionMemo, setResolutionMemo] = useState('');
  const [resolutionOutcome, setResolutionOutcome] = useState<'release_both' | 'refund_both' | 'release_base_refund_quote' | 'release_quote_refund_base'>('release_both');

  const [manualTradeId, setManualTradeId] = useState('');
  const [manualTradeError, setManualTradeError] = useState<string | null>(null);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    setOffers(restore(storageOffersKey, []));
    setTrades(restore(storageTradesKey, []));
  }, []);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    persist(storageOffersKey, offers);
  }, [offers]);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    persist(storageTradesKey, trades);
  }, [trades]);

  useEffect(() => {
    if (!sellerKey.trim()) {
      setSellerAddress('');
      setSellerAccount(null);
      setSellerError(null);
      return;
    }
    try {
      const nextAddress = deriveAddressFromPrivateKey(sellerKey);
      setSellerAddress(nextAddress);
      setSellerError(null);
    } catch (error) {
      setSellerAddress('');
      setSellerError((error as Error).message);
    }
  }, [sellerKey]);

  useEffect(() => {
    if (!buyerKey.trim()) {
      setBuyerAddress('');
      setBuyerAccount(null);
      setBuyerError(null);
      return;
    }
    try {
      const nextAddress = deriveAddressFromPrivateKey(buyerKey);
      setBuyerAddress(nextAddress);
      setBuyerError(null);
    } catch (error) {
      setBuyerAddress('');
      setBuyerError((error as Error).message);
    }
  }, [buyerKey]);

  useEffect(() => {
    if (!sellerAddress) return;
    let cancelled = false;
    async function load() {
      try {
        const account = await fetchJson<BalanceResponse>(`/api/account?address=${encodeURIComponent(sellerAddress)}`);
        if (!cancelled) {
          setSellerAccount(account);
          setSellerError(null);
        }
      } catch (error) {
        if (!cancelled) {
          setSellerAccount(null);
          setSellerError((error as Error).message);
        }
      }
    }
    load();
    return () => {
      cancelled = true;
    };
  }, [sellerAddress]);

  useEffect(() => {
    if (!buyerAddress) return;
    let cancelled = false;
    async function load() {
      try {
        const account = await fetchJson<BalanceResponse>(`/api/account?address=${encodeURIComponent(buyerAddress)}`);
        if (!cancelled) {
          setBuyerAccount(account);
          setBuyerError(null);
        }
      } catch (error) {
        if (!cancelled) {
          setBuyerAccount(null);
          setBuyerError((error as Error).message);
        }
      }
    }
    load();
    return () => {
      cancelled = true;
    };
  }, [buyerAddress]);

  const refreshTrade = useCallback(async (tradeId: string) => {
    try {
      const trade = await fetchJson<TradeSnapshot>(`/api/p2p/trade?tradeId=${encodeURIComponent(tradeId)}`);
      const [baseEscrow, quoteEscrow] = await Promise.all([
        fetchJson<EscrowSnapshot>(`/api/escrow/get?escrowId=${encodeURIComponent(trade.escrowBaseId)}`),
        fetchJson<EscrowSnapshot>(`/api/escrow/get?escrowId=${encodeURIComponent(trade.escrowQuoteId)}`)
      ]);
      setTrades((current) =>
        current.map((entry) =>
          entry.tradeId === tradeId
            ? {
                ...entry,
                status: trade.status,
                baseEscrow,
                quoteEscrow,
                lastUpdated: Date.now()
              }
            : entry
        )
      );
    } catch (error) {
      setTrades((current) =>
        current.map((entry) =>
          entry.tradeId === tradeId
            ? {
                ...entry,
                error: (error as Error).message,
                lastUpdated: Date.now()
              }
            : entry
        )
      );
    }
  }, []);

  useEffect(() => {
    if (trades.length === 0) return;
    const interval = setInterval(() => {
      trades.forEach((trade) => {
        void refreshTrade(trade.tradeId);
      });
    }, 8000);
    return () => clearInterval(interval);
  }, [trades, refreshTrade]);

  const handleCreateOffer = async () => {
    if (!sellerAddress) {
      setOfferError('Load a seller private key first.');
      return;
    }
    try {
      const baseToken = offerDirection === 'SELL_NHB' ? 'NHB' : 'ZNHB';
      const quoteToken = offerDirection === 'SELL_NHB' ? 'ZNHB' : 'NHB';
      const baseAmount = toBaseUnits(baseAmountInput);
      const quoteAmount = toBaseUnits(quoteAmountInput);
      const offer: OfferRecord = {
        id: `OFF_${Date.now()}`,
        seller: sellerAddress,
        direction: offerDirection,
        baseToken,
        quoteToken,
        baseAmount,
        quoteAmount,
        baseDisplay: baseAmountInput,
        quoteDisplay: quoteAmountInput,
        deadlineHours,
        terms: offerTerms.trim() || undefined,
        createdAt: Date.now()
      };
      setOffers((current) => [offer, ...current]);
      setOfferError(null);
    } catch (error) {
      setOfferError((error as Error).message);
    }
  };

  const handleAcceptOffer = async (offer: OfferRecord) => {
    if (!buyerAddress) {
      setAcceptError('Load a buyer private key first.');
      return;
    }
    setAcceptingOfferId(offer.id);
    setAcceptError(null);
    try {
      const deadline = Math.floor(Date.now() / 1000) + offer.deadlineHours * 3600;
      const payload = {
        offerId: offer.id,
        buyer: buyerAddress,
        seller: offer.seller,
        baseToken: offer.baseToken,
        baseAmount: offer.baseAmount,
        quoteToken: offer.quoteToken,
        quoteAmount: offer.quoteAmount,
        deadline
      };
      const result = await fetchJson<TradeCreationResponse>('/api/p2p/create-trade', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(payload)
      });
      const record: TradeRecord = {
        tradeId: result.tradeId,
        offerId: offer.id,
        buyer: buyerAddress,
        seller: offer.seller,
        baseToken: offer.baseToken,
        quoteToken: offer.quoteToken,
        baseAmount: offer.baseAmount,
        quoteAmount: offer.quoteAmount,
        escrowBaseId: result.escrowBaseId,
        escrowQuoteId: result.escrowQuoteId,
        payIntents: result.payIntents,
        createdAt: Date.now(),
        deadline,
        status: 'init'
      };
      setTrades((current) => [record, ...current]);
      await refreshTrade(result.tradeId);
    } catch (error) {
      setAcceptError((error as Error).message);
    } finally {
      setAcceptingOfferId(null);
    }
  };

  const handleFundEscrow = async (trade: TradeRecord, leg: 'base' | 'quote') => {
    const escrowId = leg === 'base' ? trade.escrowBaseId : trade.escrowQuoteId;
    const fromAddress = leg === 'base' ? trade.seller : trade.buyer;
    setFundingEscrow(escrowId);
    setFundError(null);
    try {
      await fetchJson('/api/escrow/fund', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ escrowId, from: fromAddress })
      });
      await refreshTrade(trade.tradeId);
    } catch (error) {
      setFundError((error as Error).message);
    } finally {
      setFundingEscrow(null);
    }
  };

  const handleSettleTrade = async (trade: TradeRecord) => {
    setSettlingTrade(trade.tradeId);
    setSettleError(null);
    try {
      const caller = buyerAddress && trade.buyer === buyerAddress ? buyerAddress : sellerAddress;
      if (!caller) {
        throw new Error('Load either the buyer or seller private key to settle.');
      }
      await fetchJson('/api/p2p/settle', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ tradeId: trade.tradeId, caller })
      });
      await refreshTrade(trade.tradeId);
    } catch (error) {
      setSettleError((error as Error).message);
    } finally {
      setSettlingTrade(null);
    }
  };

  const handleDispute = async (trade: TradeRecord, reason: string) => {
    setDisputeError(null);
    try {
      const caller = buyerAddress && trade.buyer === buyerAddress ? buyerAddress : sellerAddress;
      if (!caller) {
        throw new Error('Load the buyer or seller private key to dispute.');
      }
      await fetchJson('/api/p2p/dispute', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ tradeId: trade.tradeId, caller, message: reason })
      });
      await refreshTrade(trade.tradeId);
    } catch (error) {
      setDisputeError((error as Error).message);
    }
  };

  const handleResolve = async (trade: TradeRecord) => {
    setResolveError(null);
    try {
      const caller = sellerAddress || buyerAddress;
      if (!caller) {
        throw new Error('Load any authorised arbitrator address to resolve.');
      }
      await fetchJson('/api/p2p/resolve', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          tradeId: trade.tradeId,
          caller,
          outcome: resolutionOutcome,
          memo: resolutionMemo.trim() || undefined
        })
      });
      await refreshTrade(trade.tradeId);
    } catch (error) {
      setResolveError((error as Error).message);
    }
  };

  const handleTrackTrade = async () => {
    if (!manualTradeId.trim()) {
      setManualTradeError('Trade ID is required');
      return;
    }
    try {
      const trade = await fetchJson<TradeSnapshot>(`/api/p2p/trade?tradeId=${encodeURIComponent(manualTradeId.trim())}`);
      const [baseEscrow, quoteEscrow] = await Promise.all([
        fetchJson<EscrowSnapshot>(`/api/escrow/get?escrowId=${encodeURIComponent(trade.escrowBaseId)}`),
        fetchJson<EscrowSnapshot>(`/api/escrow/get?escrowId=${encodeURIComponent(trade.escrowQuoteId)}`)
      ]);
      const record: TradeRecord = {
        tradeId: trade.id,
        offerId: trade.offerId,
        buyer: trade.buyer,
        seller: trade.seller,
        baseToken: trade.baseToken,
        quoteToken: trade.quoteToken,
        baseAmount: trade.baseAmount,
        quoteAmount: trade.quoteAmount,
        escrowBaseId: trade.escrowBaseId,
        escrowQuoteId: trade.escrowQuoteId,
        payIntents: {},
        createdAt: Date.now(),
        deadline: trade.deadline,
        status: trade.status,
        baseEscrow,
        quoteEscrow
      };
      setTrades((current) => {
        const exists = current.some((entry) => entry.tradeId === record.tradeId);
        return exists ? current : [record, ...current];
      });
      setManualTradeError(null);
    } catch (error) {
      setManualTradeError((error as Error).message);
    }
  };

  const sortedTrades = useMemo(
    () =>
      [...trades].sort((a, b) => (b.createdAt || 0) - (a.createdAt || 0)),
    [trades]
  );

  return (
    <div>
      <header>
        <h1>NHB P2P Mini-Market</h1>
        <p className="muted">
          Compose buy and sell offers for NHB ⇄ ZNHB, accept trades, fund both legs, and settle atomically via dual-lock escrow.
        </p>
      </header>

      <section>
        <h2>Wallets</h2>
        <div className="grid columns-2">
          <div>
            <h3>Seller</h3>
            <label htmlFor="seller-key">Private key</label>
            <textarea
              id="seller-key"
              className="small"
              value={sellerKey}
              onChange={(event) => setSellerKey(event.target.value)}
              placeholder="0x..."
            />
            <div className="flex-row">
              <button type="button" onClick={() => setSellerKey(randomPrivateKey())}>Generate</button>
            </div>
            {sellerError ? <p className="badge warning">{sellerError}</p> : null}
            {sellerAddress ? (
              <div className="card-list-item">
                <strong>Address</strong>
                <p>{sellerAddress}</p>
                {sellerAccount ? (
                  <div className="status-grid" style={{ marginTop: '0.75rem' }}>
                    <div className="status-item">
                      <strong>NHB</strong>
                      <span>{formatAmount(sellerAccount.balanceNHB)}</span>
                    </div>
                    <div className="status-item">
                      <strong>ZNHB</strong>
                      <span>{formatAmount(sellerAccount.balanceZNHB)}</span>
                    </div>
                  </div>
                ) : null}
              </div>
            ) : null}
          </div>

          <div>
            <h3>Buyer</h3>
            <label htmlFor="buyer-key">Private key</label>
            <textarea
              id="buyer-key"
              className="small"
              value={buyerKey}
              onChange={(event) => setBuyerKey(event.target.value)}
              placeholder="0x..."
            />
            <div className="flex-row">
              <button type="button" onClick={() => setBuyerKey(randomPrivateKey())}>Generate</button>
            </div>
            {buyerError ? <p className="badge warning">{buyerError}</p> : null}
            {buyerAddress ? (
              <div className="card-list-item">
                <strong>Address</strong>
                <p>{buyerAddress}</p>
                {buyerAccount ? (
                  <div className="status-grid" style={{ marginTop: '0.75rem' }}>
                    <div className="status-item">
                      <strong>NHB</strong>
                      <span>{formatAmount(buyerAccount.balanceNHB)}</span>
                    </div>
                    <div className="status-item">
                      <strong>ZNHB</strong>
                      <span>{formatAmount(buyerAccount.balanceZNHB)}</span>
                    </div>
                  </div>
                ) : null}
              </div>
            ) : null}
          </div>
        </div>
      </section>

      <section>
        <h2>Create Offer</h2>
        <fieldset>
          <legend>Direction</legend>
          <div className="flex-row">
            <label>
              <input
                type="radio"
                name="direction"
                value="SELL_NHB"
                checked={offerDirection === 'SELL_NHB'}
                onChange={() => setOfferDirection('SELL_NHB')}
              />
              <span style={{ marginLeft: '0.5rem' }}>Sell NHB for ZNHB</span>
            </label>
            <label>
              <input
                type="radio"
                name="direction"
                value="SELL_ZNHB"
                checked={offerDirection === 'SELL_ZNHB'}
                onChange={() => setOfferDirection('SELL_ZNHB')}
              />
              <span style={{ marginLeft: '0.5rem' }}>Sell ZNHB for NHB</span>
            </label>
          </div>
        </fieldset>
        <label htmlFor="base-amount">Sell amount ({offerDirection === 'SELL_NHB' ? 'NHB' : 'ZNHB'})</label>
        <input id="base-amount" value={baseAmountInput} onChange={(event) => setBaseAmountInput(event.target.value)} />
        <label htmlFor="quote-amount">Buy amount ({offerDirection === 'SELL_NHB' ? 'ZNHB' : 'NHB'})</label>
        <input id="quote-amount" value={quoteAmountInput} onChange={(event) => setQuoteAmountInput(event.target.value)} />
        <label htmlFor="deadline-hours">Funding deadline (hours)</label>
        <input
          id="deadline-hours"
          type="number"
          min={1}
          max={168}
          value={deadlineHours}
          onChange={(event) => setDeadlineHours(Number(event.target.value))}
        />
        <label htmlFor="offer-terms">Terms (optional)</label>
        <textarea id="offer-terms" className="small" value={offerTerms} onChange={(event) => setOfferTerms(event.target.value)} />
        <button type="button" onClick={handleCreateOffer} disabled={!sellerAddress}>
          Publish Offer
        </button>
        {offerError ? <p className="badge warning" style={{ marginTop: '0.75rem' }}>{offerError}</p> : null}
      </section>

      <section>
        <h2>Open Offers</h2>
        {offers.length === 0 ? <p className="muted">No offers yet. Create one above to seed the market.</p> : null}
        <div className="card-list">
          {offers.map((offer) => (
            <div className="card-list-item" key={offer.id}>
              <div className="flex-row" style={{ justifyContent: 'space-between' }}>
                <div>
                  <strong>{offer.direction === 'SELL_NHB' ? 'Sell NHB' : 'Sell ZNHB'}</strong>
                  <p className="muted">Offer ID: {offer.id}</p>
                </div>
                <span className="badge">Created {formatTimestamp(Math.floor(offer.createdAt / 1000))}</span>
              </div>
              <div className="table-scroll" style={{ marginTop: '0.75rem' }}>
                <table>
                  <tbody>
                    <tr>
                      <th>Seller</th>
                      <td>{offer.seller}</td>
                    </tr>
                    <tr>
                      <th>Base</th>
                      <td>
                        {offer.baseDisplay} {offer.baseToken}
                      </td>
                    </tr>
                    <tr>
                      <th>Quote</th>
                      <td>
                        {offer.quoteDisplay} {offer.quoteToken}
                      </td>
                    </tr>
                    <tr>
                      <th>Deadline</th>
                      <td>{offer.deadlineHours} hours</td>
                    </tr>
                    {offer.terms ? (
                      <tr>
                        <th>Terms</th>
                        <td>{offer.terms}</td>
                      </tr>
                    ) : null}
                  </tbody>
                </table>
              </div>
              <button
                type="button"
                style={{ marginTop: '1rem' }}
                disabled={acceptingOfferId === offer.id || !buyerAddress}
                onClick={() => void handleAcceptOffer(offer)}
              >
                {acceptingOfferId === offer.id ? 'Creating trade…' : 'Accept offer'}
              </button>
            </div>
          ))}
        </div>
        {acceptError ? <p className="badge warning" style={{ marginTop: '1rem' }}>{acceptError}</p> : null}
      </section>

      <section>
        <h2>Active Trades</h2>
        <div className="flex-row" style={{ marginBottom: '1rem' }}>
          <input
            placeholder="Paste trade ID to track"
            value={manualTradeId}
            onChange={(event) => setManualTradeId(event.target.value)}
            style={{ flex: '1 1 320px' }}
          />
          <button type="button" onClick={() => void handleTrackTrade()}>
            Track trade
          </button>
        </div>
        {manualTradeError ? <p className="badge warning">{manualTradeError}</p> : null}
        {sortedTrades.length === 0 ? <p className="muted">Accept an offer to see the dual-lock workflow.</p> : null}
        <div className="card-list">
          {sortedTrades.map((trade) => (
            <div className="card-list-item" key={trade.tradeId}>
              <div className="flex-row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
                <div>
                  <strong>Trade {trade.tradeId}</strong>
                  <p className="muted">Status: {formatStatus(trade.status)}</p>
                </div>
                <span className={`badge ${trade.status === 'settled' ? 'success' : ''}`}>
                  Last update {trade.lastUpdated ? formatTimestamp(Math.floor(trade.lastUpdated / 1000)) : 'pending'}
                </span>
              </div>
              <div className="table-scroll" style={{ marginTop: '0.75rem' }}>
                <table>
                  <tbody>
                    <tr>
                      <th>Buyer</th>
                      <td>{trade.buyer}</td>
                    </tr>
                    <tr>
                      <th>Seller</th>
                      <td>{trade.seller}</td>
                    </tr>
                    <tr>
                      <th>Base leg</th>
                      <td>
                        {formatAmount(trade.baseAmount)} {trade.baseToken} → Escrow {trade.escrowBaseId}
                      </td>
                    </tr>
                    <tr>
                      <th>Quote leg</th>
                      <td>
                        {formatAmount(trade.quoteAmount)} {trade.quoteToken} → Escrow {trade.escrowQuoteId}
                      </td>
                    </tr>
                    <tr>
                      <th>Deadline</th>
                      <td>{formatTimestamp(trade.deadline)}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <div className="intent-grid two-columns" style={{ marginTop: '1.25rem' }}>
                <PayIntentCard title="Seller deposit" intent={trade.payIntents.seller ?? null} description="Seller funds base token" />
                <PayIntentCard title="Buyer deposit" intent={trade.payIntents.buyer ?? null} description="Buyer funds quote token" />
              </div>
              <div className="status-grid" style={{ marginTop: '1.25rem' }}>
                <div className="status-item">
                  <div>
                    <strong>Base escrow</strong>
                    <div className="muted">{trade.escrowBaseId}</div>
                  </div>
                  <div>
                    <span>{formatStatus(trade.baseEscrow?.status)}</span>
                    <button
                      type="button"
                      style={{ marginLeft: '0.75rem' }}
                      onClick={() => void handleFundEscrow(trade, 'base')}
                      disabled={fundingEscrow === trade.escrowBaseId}
                    >
                      {fundingEscrow === trade.escrowBaseId ? 'Marking funded…' : 'Mark funded'}
                    </button>
                  </div>
                </div>
                <div className="status-item">
                  <div>
                    <strong>Quote escrow</strong>
                    <div className="muted">{trade.escrowQuoteId}</div>
                  </div>
                  <div>
                    <span>{formatStatus(trade.quoteEscrow?.status)}</span>
                    <button
                      type="button"
                      style={{ marginLeft: '0.75rem' }}
                      onClick={() => void handleFundEscrow(trade, 'quote')}
                      disabled={fundingEscrow === trade.escrowQuoteId}
                    >
                      {fundingEscrow === trade.escrowQuoteId ? 'Marking funded…' : 'Mark funded'}
                    </button>
                  </div>
                </div>
              </div>
              <div className="flex-row" style={{ marginTop: '1.25rem' }}>
                <button type="button" onClick={() => void handleSettleTrade(trade)} disabled={settlingTrade === trade.tradeId}>
                  {settlingTrade === trade.tradeId ? 'Settling…' : 'Settle trade'}
                </button>
                <button type="button" onClick={() => void handleDispute(trade, 'Manual dispute triggered from demo UI')}>
                  Dispute
                </button>
              </div>
              {trade.status === 'disputed' ? (
                <div style={{ marginTop: '1.25rem' }}>
                  <h4>Resolve dispute</h4>
                  <label htmlFor={`resolution-outcome-${trade.tradeId}`}>Outcome</label>
                  <select
                    id={`resolution-outcome-${trade.tradeId}`}
                    value={resolutionOutcome}
                    onChange={(event) => setResolutionOutcome(event.target.value as typeof resolutionOutcome)}
                  >
                    <option value="release_both">Release both escrows</option>
                    <option value="refund_both">Refund both escrows</option>
                    <option value="release_base_refund_quote">Release base, refund quote</option>
                    <option value="release_quote_refund_base">Release quote, refund base</option>
                  </select>
                  <label htmlFor={`resolution-memo-${trade.tradeId}`}>Resolution memo</label>
                  <textarea
                    id={`resolution-memo-${trade.tradeId}`}
                    className="small"
                    value={resolutionMemo}
                    onChange={(event) => setResolutionMemo(event.target.value)}
                  />
                  <button type="button" onClick={() => void handleResolve(trade)}>
                    Submit resolution
                  </button>
                </div>
              ) : null}
              {trade.error ? <p className="badge warning" style={{ marginTop: '1rem' }}>{trade.error}</p> : null}
            </div>
          ))}
        </div>
        {fundError ? <p className="badge warning" style={{ marginTop: '1rem' }}>{fundError}</p> : null}
        {settleError ? <p className="badge warning" style={{ marginTop: '1rem' }}>{settleError}</p> : null}
        {disputeError ? <p className="badge warning" style={{ marginTop: '1rem' }}>{disputeError}</p> : null}
        {resolveError ? <p className="badge warning" style={{ marginTop: '1rem' }}>{resolveError}</p> : null}
      </section>
    </div>
  );
}
