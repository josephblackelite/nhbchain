'use client';

import { useEffect, useMemo, useState } from 'react';
import QRCode from 'qrcode.react';
import { aliasFingerprint } from '../lib/identity';
import { deriveAddressFromPrivateKey, randomPrivateKey } from '../lib/wallet';

interface BalanceResponse {
  address: string;
  balanceNHB: string;
  balanceZNHB: string;
  stake: string;
  lockedZNHB: string;
  delegatedValidator?: string;
  username?: string;
  nonce: number;
  engagementScore: number;
}

interface ClaimableResult {
  claimId: string;
  recipientHint: string;
  token: string;
  amount: string;
  expiresAt: number;
  createdAt: number;
}

interface IdentityResolveResult {
  alias: string;
  aliasId: string;
  primary: string;
  addresses: string[];
  avatarRef?: string;
  createdAt: number;
  updatedAt: number;
}

interface CreatorProfile {
  alias: string;
  primary: string;
  addresses: string[];
  avatarUrl: string | null;
  createdAt: number;
  updatedAt: number;
  recentContent: Array<{
    id?: string;
    title?: string;
    uri?: string;
    publishedAt?: string;
    tippedAt?: string;
  }>;
}

interface CreatorTipResult {
  contentId: string;
  creator: string;
  fan: string;
  amount: string;
  totalTips?: string;
  totalYield?: string;
  pending?: string;
}

interface CreatorStakeResult {
  creator: string;
  fan: string;
  amount: string;
  shares?: string;
  pending?: string;
  totalTips?: string;
  totalYield?: string;
}

interface CreatorUnstakeResult {
  creator: string;
  fan: string;
  amount: string;
  remaining?: string;
  shares?: string;
}

interface SubscriptionPreview {
  alias: string;
  amount: string;
  cadence: 'weekly' | 'monthly';
  occurrences: Array<{ dueAt: string; amount: string }>;
}

interface EscrowDisputeView {
  id: string;
  status: string;
  payer: string;
  payee: string;
  token: string;
  amount: string;
  payeeAlias?: string;
  payeeAliasId?: string;
}

function formatNumber(value?: string) {
  if (!value) return '0';
  const trimmed = value.replace(/^0+/, '') || '0';
  if (trimmed.length <= 18) {
    return `0.${trimmed.padStart(18, '0')}`.replace(/\.0+$/, '.0');
  }
  const whole = trimmed.slice(0, trimmed.length - 18);
  const fraction = trimmed.slice(-18).replace(/0+$/, '');
  return fraction ? `${whole}.${fraction}` : whole;
}

function formatDate(ts: number) {
  return new Date(ts * 1000).toLocaleString();
}

export default function WalletLiteApp() {
  const [privateKey, setPrivateKey] = useState('');
  const [address, setAddress] = useState('');
  const [account, setAccount] = useState<BalanceResponse | null>(null);
  const [accountError, setAccountError] = useState<string | null>(null);
  const [aliasInput, setAliasInput] = useState('');
  const [aliasStatus, setAliasStatus] = useState<string | null>(null);
  const [aliasError, setAliasError] = useState<string | null>(null);
  const [claimableStatus, setClaimableStatus] = useState<ClaimableResult | null>(null);
  const [claimableError, setClaimableError] = useState<string | null>(null);
  const [claimStatus, setClaimStatus] = useState<string | null>(null);
  const [claimError, setClaimError] = useState<string | null>(null);
  const [recipientType, setRecipientType] = useState<'alias' | 'email' | 'hash'>('alias');
  const [recipientAlias, setRecipientAlias] = useState('');
  const [recipientEmail, setRecipientEmail] = useState('');
  const [recipientHash, setRecipientHash] = useState('');
  const [amount, setAmount] = useState('1.0');
  const [token, setToken] = useState<'NHB' | 'ZNHB'>('NHB');
  const [deadlineHours, setDeadlineHours] = useState(168);
  const [claimId, setClaimId] = useState('');
  const [claimAlias, setClaimAlias] = useState('');
  const [claimPreimage, setClaimPreimage] = useState('');
  const [resolvingAlias, setResolvingAlias] = useState<IdentityResolveResult | null>(null);
  const [resolveError, setResolveError] = useState<string | null>(null);
  const [profileAlias, setProfileAlias] = useState('');
  const [profile, setProfile] = useState<CreatorProfile | null>(null);
  const [profileLoading, setProfileLoading] = useState(false);
  const [profileError, setProfileError] = useState<string | null>(null);
  const [tipContentId, setTipContentId] = useState('demo-drop');
  const [tipAmount, setTipAmount] = useState('0.1');
  const [tipStatus, setTipStatus] = useState<CreatorTipResult | null>(null);
  const [tipError, setTipError] = useState<string | null>(null);
  const [subscriptionCreator, setSubscriptionCreator] = useState('');
  const [subscriptionAmount, setSubscriptionAmount] = useState('1.0');
  const [subscriptionCadence, setSubscriptionCadence] = useState<'weekly' | 'monthly'>('monthly');
  const [subscriptionStart, setSubscriptionStart] = useState(() => new Date().toISOString().slice(0, 10));
  const [subscriptionStatus, setSubscriptionStatus] = useState<CreatorStakeResult | null>(null);
  const [subscriptionError, setSubscriptionError] = useState<string | null>(null);
  const [unstakeAmount, setUnstakeAmount] = useState('1.0');
  const [unstakeStatus, setUnstakeStatus] = useState<CreatorUnstakeResult | null>(null);
  const [unstakeError, setUnstakeError] = useState<string | null>(null);
  const [subscriptionPreview, setSubscriptionPreview] = useState<SubscriptionPreview | null>(null);
  const [subscriptionPreviewError, setSubscriptionPreviewError] = useState<string | null>(null);
  const [disputeEscrowId, setDisputeEscrowId] = useState('');
  const [disputeDetails, setDisputeDetails] = useState<EscrowDisputeView | null>(null);
  const [disputeLoading, setDisputeLoading] = useState(false);
  const [disputeError, setDisputeError] = useState<string | null>(null);
  const [disputeReason, setDisputeReason] = useState('');
  const [disputeMarking, setDisputeMarking] = useState(false);
  const [disputeSuccess, setDisputeSuccess] = useState<string | null>(null);
  const [markAsScam, setMarkAsScam] = useState(false);

  useEffect(() => {
    if (!address) {
      setAccount(null);
      return;
    }
    let cancelled = false;
    async function fetchAccount() {
      try {
        setAccountError(null);
        const res = await fetch(`/api/account?address=${encodeURIComponent(address)}`);
        if (!res.ok) {
          const data = await res.json();
          throw new Error(data?.error || 'Failed to load account');
        }
        const data = (await res.json()) as BalanceResponse;
        if (!cancelled) {
          setAccount(data);
        }
      } catch (error) {
        if (!cancelled) {
          setAccount(null);
          setAccountError((error as Error).message);
        }
      }
    }
    fetchAccount();
    return () => {
      cancelled = true;
    };
  }, [address]);

  useEffect(() => {
    const alias = recipientAlias.trim();
    if (!alias) {
      setResolvingAlias(null);
      setResolveError(null);
      return;
    }
    let cancelled = false;
    async function resolve() {
      try {
        setResolveError(null);
        const res = await fetch(`/api/identity/resolve?alias=${encodeURIComponent(alias)}`);
        if (!res.ok) {
          const data = await res.json();
          throw new Error(data?.error || 'Alias not found');
        }
        const payload = (await res.json()) as IdentityResolveResult;
        if (!cancelled) {
          setResolvingAlias(payload);
        }
      } catch (error) {
        if (!cancelled) {
          setResolvingAlias(null);
          setResolveError((error as Error).message);
        }
      }
    }
    resolve();
    return () => {
      cancelled = true;
    };
  }, [recipientAlias]);

  useEffect(() => {
    const alias = profileAlias.trim();
    if (!alias) {
      setProfile(null);
      setProfileError(null);
      setProfileLoading(false);
      return;
    }
    let cancelled = false;
    setProfileLoading(true);
    async function loadProfile() {
      try {
        const res = await fetch(`/api/identity/profile?alias=${encodeURIComponent(alias)}`);
        if (!res.ok) {
          const data = await res.json();
          throw new Error(data?.error || 'Profile not found');
        }
        const payload = (await res.json()) as CreatorProfile;
        if (!cancelled) {
          setProfile(payload);
          setProfileError(null);
        }
      } catch (error) {
        if (!cancelled) {
          setProfile(null);
          setProfileError((error as Error).message);
        }
      } finally {
        if (!cancelled) {
          setProfileLoading(false);
        }
      }
    }
    loadProfile();
    return () => {
      cancelled = true;
    };
  }, [profileAlias]);

  const paymentIntent = useMemo(() => {
    if (!recipientAlias.trim()) return '';
    const params = new URLSearchParams({
      to: `@${recipientAlias.trim()}`,
      token,
      amount: amount.trim(),
    });
    return `znhb://pay?${params.toString()}`;
  }, [recipientAlias, amount, token]);

  const handlePrivateKeyChange = (value: string) => {
    setPrivateKey(value);
    try {
      const nextAddress = deriveAddressFromPrivateKey(value);
      setAddress(nextAddress);
    } catch (error) {
      setAddress('');
      setAccount(null);
      setAccountError((error as Error).message);
    }
  };

  const handleRegisterAlias = async () => {
    if (!address) {
      setAliasError('Address required');
      return;
    }
    if (!aliasInput.trim()) {
      setAliasError('Alias required');
      return;
    }
    try {
      setAliasError(null);
      const res = await fetch('/api/identity/set-alias', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ address, alias: aliasInput.trim() })
      });
      const payload = await res.json();
      if (!res.ok) {
        throw new Error(payload?.error || 'Failed to register alias');
      }
      setAliasStatus(`Alias @${aliasInput.trim()} registered`);
      setAliasInput('');
    } catch (error) {
      setAliasStatus(null);
      setAliasError((error as Error).message);
    }
  };

  const handleCreateClaimable = async () => {
    if (!address) {
      setClaimableError('Provide the payer address by loading a private key.');
      return;
    }
    try {
      setClaimableError(null);
      setClaimableStatus(null);
      const res = await fetch('/api/payments/claimables', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          payer: address,
          token,
          amount,
          deadlineHours,
          recipientType,
          alias: recipientAlias.trim() || undefined,
          email: recipientEmail.trim() || undefined,
          recipientHash: recipientHash.trim() || undefined
        })
      });
      const payload = await res.json();
      if (!res.ok) {
        throw new Error(payload?.error || 'Failed to create claimable');
      }
      setClaimableStatus(payload as ClaimableResult);
    } catch (error) {
      setClaimableStatus(null);
      setClaimableError((error as Error).message);
    }
  };

  const handleClaim = async () => {
    if (!claimId.trim() || !claimAlias.trim() && !claimPreimage.trim()) {
      setClaimError('Claim ID and either alias or preimage required');
      return;
    }
    if (!address) {
      setClaimError('Set the local payee address first.');
      return;
    }
    try {
      setClaimError(null);
      setClaimStatus(null);
      const res = await fetch('/api/identity/claim', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          claimId: claimId.trim(),
          payee: address,
          alias: claimAlias.trim() || undefined,
          preimage: claimPreimage.trim() || undefined
        })
      });
      const payload = await res.json();
      if (!res.ok) {
        throw new Error(payload?.error || 'Claim failed');
      }
      const claimed = payload?.amount ? `${formatNumber(payload.amount)} ${payload.token}` : 'Claim';
      setClaimStatus(`${claimed} submitted successfully`);
    } catch (error) {
      setClaimStatus(null);
      setClaimError((error as Error).message);
    }
  };

  const handleSendTip = async () => {
    if (!address) {
      setTipError('Load a wallet before sending tips.');
      return;
    }
    if (!tipContentId.trim()) {
      setTipError('Content ID required');
      return;
    }
    try {
      setTipError(null);
      setTipStatus(null);
      const res = await fetch('/api/creator/tips', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ caller: address, contentId: tipContentId.trim(), amount: tipAmount.trim() })
      });
      const payload = await res.json();
      if (!res.ok) {
        throw new Error(payload?.error || 'Tip failed');
      }
      setTipStatus(payload as CreatorTipResult);
    } catch (error) {
      setTipStatus(null);
      setTipError((error as Error).message);
    }
  };

  const handleStake = async () => {
    if (!address) {
      setSubscriptionError('Load a wallet before subscribing.');
      return;
    }
    if (!subscriptionCreator.trim()) {
      setSubscriptionError('Creator address required');
      return;
    }
    try {
      setSubscriptionError(null);
      setSubscriptionStatus(null);
      const res = await fetch('/api/creator/stake', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ caller: address, creator: subscriptionCreator.trim(), amount: subscriptionAmount.trim() })
      });
      const payload = await res.json();
      if (!res.ok) {
        throw new Error(payload?.error || 'Subscription stake failed');
      }
      setSubscriptionStatus(payload as CreatorStakeResult);
    } catch (error) {
      setSubscriptionStatus(null);
      setSubscriptionError((error as Error).message);
    }
  };

  const handleUnstake = async () => {
    if (!address) {
      setUnstakeError('Load a wallet before managing subscriptions.');
      return;
    }
    if (!subscriptionCreator.trim()) {
      setUnstakeError('Creator address required');
      return;
    }
    try {
      setUnstakeError(null);
      setUnstakeStatus(null);
      const res = await fetch('/api/creator/unstake', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ caller: address, creator: subscriptionCreator.trim(), amount: unstakeAmount.trim() })
      });
      const payload = await res.json();
      if (!res.ok) {
        throw new Error(payload?.error || 'Unstake failed');
      }
      setUnstakeStatus(payload as CreatorUnstakeResult);
    } catch (error) {
      setUnstakeStatus(null);
      setUnstakeError((error as Error).message);
    }
  };

  const handlePreviewSubscription = async () => {
    if (!profileAlias.trim() && !recipientAlias.trim()) {
      setSubscriptionPreview(null);
      setSubscriptionPreviewError('Provide a creator alias to preview.');
      return;
    }
    const alias = profileAlias.trim() || recipientAlias.trim();
    try {
      setSubscriptionPreviewError(null);
      const res = await fetch('/api/creator/subscriptions', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          alias,
          amount: subscriptionAmount.trim(),
          cadence: subscriptionCadence,
          startDate: subscriptionStart
        })
      });
      const payload = await res.json();
      if (!res.ok) {
        throw new Error(payload?.error || 'Preview failed');
      }
      setSubscriptionPreview(payload as SubscriptionPreview);
    } catch (error) {
      setSubscriptionPreview(null);
      setSubscriptionPreviewError((error as Error).message);
    }
  };

  const handleLoadEscrowDetails = async () => {
    const id = disputeEscrowId.trim();
    if (!id) {
      setDisputeDetails(null);
      setDisputeError('Escrow ID required');
      return;
    }
    try {
      setDisputeLoading(true);
      setDisputeError(null);
      setDisputeSuccess(null);
      setMarkAsScam(false);
      const res = await fetch(`/api/escrow/${encodeURIComponent(id)}`);
      const payload = await res.json();
      if (!res.ok) {
        throw new Error(payload?.error || 'Failed to load escrow');
      }
      setDisputeDetails(payload as EscrowDisputeView);
    } catch (error) {
      setDisputeDetails(null);
      setDisputeError((error as Error).message);
    } finally {
      setDisputeLoading(false);
    }
  };

  const handleMarkAsScamToggle = async (checked: boolean) => {
    setMarkAsScam(checked);
    if (!checked) {
      return;
    }
    if (!disputeDetails) {
      setDisputeError('Load escrow details before flagging a dispute.');
      setMarkAsScam(false);
      return;
    }
    try {
      setDisputeMarking(true);
      setDisputeError(null);
      const res = await fetch(`/api/escrow/${encodeURIComponent(disputeDetails.id)}/mark-scam`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ reason: disputeReason })
      });
      const payload = await res.json();
      if (!res.ok) {
        throw new Error(payload?.error || 'Failed to submit dispute');
      }
      setDisputeSuccess('Escrow dispute submitted to the node.');
    } catch (error) {
      setMarkAsScam(false);
      setDisputeSuccess(null);
      setDisputeError((error as Error).message);
    } finally {
      setDisputeMarking(false);
    }
  };

  return (
    <div className="grid columns-2">
      <section>
        <h2>Local session</h2>
        <p>
          Wallet Lite stores the private key only in memory. Use throwaway credentials on testnet endpoints and refresh the page to clear the session.
        </p>
        <label htmlFor="privateKey">Private key (hex)</label>
        <input
          id="privateKey"
          placeholder="0x..."
          value={privateKey}
          onChange={(event) => handlePrivateKeyChange(event.target.value)}
        />
        <div className="form-footer">
          <button type="button" onClick={() => handlePrivateKeyChange(randomPrivateKey())}>
            Generate demo key
          </button>
          <small>{address ? `Derived address: ${address}` : accountError}</small>
        </div>
      </section>

      <section>
        <h2>Account snapshot</h2>
        {address ? (
          account ? (
            <div className="card-list">
              <div className="card-list-item">
                <strong>Address</strong>
                <div className="code-inline">{address}</div>
              </div>
              <div className="card-list-item">
                <strong>NHB balance</strong>
                <div>{formatNumber(account.balanceNHB)} NHB</div>
              </div>
              <div className="card-list-item">
                <strong>ZNHB balance</strong>
                <div>{formatNumber(account.balanceZNHB)} ZNHB</div>
              </div>
              <div className="card-list-item">
                <strong>Alias</strong>
                <div>{account.username || '—'}</div>
              </div>
            </div>
          ) : (
            <p>{accountError || 'Loading account…'}</p>
          )
        ) : (
          <p>Provide a private key to load account details.</p>
        )}
      </section>

      <section>
        <h2>Creator profile</h2>
        <p>Resolve a public alias to surface their avatar, linked addresses, and recent content from the NHB gateway.</p>
        <label htmlFor="profileAlias">Creator alias</label>
        <input
          id="profileAlias"
          placeholder="artist"
          value={profileAlias}
          onChange={(event) => setProfileAlias(event.target.value)}
        />
        {profileLoading && <p>Loading profile…</p>}
        {profileError && <div className="alert alert-error">{profileError}</div>}
        {profile && (
          <div className="card-list">
            <div className="card-list-item" style={{ display: 'flex', gap: '1rem', alignItems: 'center' }}>
              {profile.avatarUrl ? (
                <img
                  src={profile.avatarUrl}
                  alt={`Avatar for @${profile.alias}`}
                  style={{ width: '64px', height: '64px', borderRadius: '50%', objectFit: 'cover' }}
                />
              ) : (
                <div
                  style={{
                    width: '64px',
                    height: '64px',
                    borderRadius: '50%',
                    background: '#1e293b',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    color: '#cbd5f5',
                    fontWeight: 600,
                  }}
                >
                  @{profile.alias.slice(0, 2)}
                </div>
              )}
              <div>
                <strong>@{profile.alias}</strong>
                <div className="code-inline">Primary: {aliasFingerprint(profile.primary)}</div>
                <small>Last updated {formatDate(profile.updatedAt)}</small>
              </div>
            </div>
            <div className="card-list-item">
              <strong>Linked addresses</strong>
              <ul>
                {profile.addresses.map((addr) => (
                  <li key={addr} className="code-inline">
                    {addr}
                  </li>
                ))}
              </ul>
            </div>
            <div className="card-list-item">
              <strong>Recent content</strong>
              <ul>
                {profile.recentContent.map((item) => (
                  <li key={`${item.id}-${item.uri}`.replace(/undefined/g, 'na')}>
                    <div>{item.title || item.id || 'Untitled drop'}</div>
                    {item.uri ? (
                      <a href={item.uri} target="_blank" rel="noreferrer">
                        {item.uri}
                      </a>
                    ) : null}
                    {(item.publishedAt || item.tippedAt) && (
                      <small>
                        {item.publishedAt ? `Published ${new Date(item.publishedAt).toLocaleString()}` : null}
                        {item.publishedAt && item.tippedAt ? ' • ' : null}
                        {item.tippedAt ? `Last tipped ${new Date(item.tippedAt).toLocaleString()}` : null}
                      </small>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          </div>
        )}
      </section>

      <section>
        <h2>Register username</h2>
        <p>Claim an alias for the session address via <code className="code-inline">identity_setAlias</code>.</p>
        <label htmlFor="alias">Alias</label>
        <input
          id="alias"
          placeholder="frankrocks"
          value={aliasInput}
          onChange={(event) => setAliasInput(event.target.value)}
        />
        <button type="button" onClick={handleRegisterAlias}>Register alias</button>
        {aliasStatus && <div className="alert alert-success">{aliasStatus}</div>}
        {aliasError && <div className="alert alert-error">{aliasError}</div>}
      </section>

      <section>
        <h2>Tip a creator</h2>
        <p>Send a direct tip against published content via <code className="code-inline">creator_tip</code>.</p>
        <label htmlFor="tipContentId">Content ID</label>
        <input
          id="tipContentId"
          placeholder="demo-drop"
          value={tipContentId}
          onChange={(event) => setTipContentId(event.target.value)}
        />
        <label htmlFor="tipAmount">Amount (NHB)</label>
        <input
          id="tipAmount"
          placeholder="0.1"
          value={tipAmount}
          onChange={(event) => setTipAmount(event.target.value)}
        />
        <button type="button" onClick={handleSendTip}>Send tip</button>
        {tipStatus && (
          <div className="alert alert-success">
            Tipped {formatNumber(tipStatus.amount)} NHB to {tipStatus.creator || 'creator'}.
            <br />Pending payouts: {tipStatus.pending ? formatNumber(tipStatus.pending) : '0'} NHB
          </div>
        )}
        {tipError && <div className="alert alert-error">{tipError}</div>}
      </section>

      <section>
        <h2>Subscribe to a creator</h2>
        <p>
          Stake behind a creator to simulate a recurring membership via <code className="code-inline">creator_stake</code>.
          Preview the cadence and manage unstakes when the subscription ends.
        </p>
        <label htmlFor="subscriptionCreator">Creator address</label>
        <input
          id="subscriptionCreator"
          placeholder="nhb1creator..."
          value={subscriptionCreator}
          onChange={(event) => setSubscriptionCreator(event.target.value)}
        />
        <label htmlFor="subscriptionAmount">Stake amount (NHB)</label>
        <input
          id="subscriptionAmount"
          placeholder="1.0"
          value={subscriptionAmount}
          onChange={(event) => setSubscriptionAmount(event.target.value)}
        />
        <label htmlFor="subscriptionCadence">Cadence</label>
        <select
          id="subscriptionCadence"
          value={subscriptionCadence}
          onChange={(event) => setSubscriptionCadence(event.target.value as 'weekly' | 'monthly')}
        >
          <option value="weekly">Weekly</option>
          <option value="monthly">Monthly</option>
        </select>
        <label htmlFor="subscriptionStart">Start date</label>
        <input
          id="subscriptionStart"
          type="date"
          value={subscriptionStart}
          onChange={(event) => setSubscriptionStart(event.target.value)}
        />
        <div className="form-footer" style={{ gap: '0.75rem', flexWrap: 'wrap' }}>
          <button type="button" onClick={handlePreviewSubscription}>
            Preview schedule
          </button>
          <button type="button" onClick={handleStake}>
            Stake (subscribe)
          </button>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <label htmlFor="unstakeAmount" style={{ margin: 0 }}>
              Unstake amount
            </label>
            <input
              id="unstakeAmount"
              value={unstakeAmount}
              onChange={(event) => setUnstakeAmount(event.target.value)}
              style={{ width: '8rem' }}
            />
            <button type="button" onClick={handleUnstake}>
              Unstake
            </button>
          </div>
        </div>
        {subscriptionStatus && (
          <div className="alert alert-success">
            Staked {formatNumber(subscriptionStatus.amount)} NHB behind {subscriptionStatus.creator || 'creator'}.
          </div>
        )}
        {subscriptionError && <div className="alert alert-error">{subscriptionError}</div>}
        {unstakeStatus && (
          <div className="alert alert-success">
            Unstaked {formatNumber(unstakeStatus.amount)} NHB for {unstakeStatus.creator || 'creator'}.
            {unstakeStatus.remaining ? (
              <>
                <br />Remaining stake: {formatNumber(unstakeStatus.remaining)} NHB
              </>
            ) : null}
          </div>
        )}
        {unstakeError && <div className="alert alert-error">{unstakeError}</div>}
        {subscriptionPreview && (
          <div className="card-list" style={{ marginTop: '1rem' }}>
            <div className="card-list-item">
              <strong>Upcoming charges ({subscriptionPreview.cadence})</strong>
              <ul>
                {subscriptionPreview.occurrences.map((occurrence) => (
                  <li key={occurrence.dueAt}>
                    {new Date(occurrence.dueAt).toLocaleDateString()} • {occurrence.amount} NHB
                  </li>
                ))}
              </ul>
            </div>
          </div>
        )}
        {subscriptionPreviewError && <div className="alert alert-error">{subscriptionPreviewError}</div>}
      </section>

      <section>
        <h2>Send via claimable</h2>
        <p>Create a pay-by-alias or pay-by-email claimable using <code className="code-inline">identity_createClaimable</code>.</p>
        <label htmlFor="recipientType">Recipient type</label>
        <select
          id="recipientType"
          value={recipientType}
          onChange={(event) => setRecipientType(event.target.value as 'alias' | 'email' | 'hash')}
        >
          <option value="alias">Username</option>
          <option value="email">Email</option>
          <option value="hash">Known preimage hash</option>
        </select>

        {recipientType === 'alias' && (
          <>
            <label htmlFor="recipientAlias">Alias</label>
            <input
              id="recipientAlias"
              placeholder="recipient"
              value={recipientAlias}
              onChange={(event) => setRecipientAlias(event.target.value)}
            />
            {resolvingAlias && (
              <div className="alert alert-success">
                Primary address {aliasFingerprint(resolvingAlias.primary)} • Created {formatDate(resolvingAlias.createdAt)}
              </div>
            )}
            {resolveError && <div className="alert alert-error">{resolveError}</div>}
          </>
        )}

        {recipientType === 'email' && (
          <>
            <label htmlFor="recipientEmail">Email</label>
            <input
              id="recipientEmail"
              placeholder="user@example.com"
              value={recipientEmail}
              onChange={(event) => setRecipientEmail(event.target.value)}
            />
            <small>The server derives the salted email hash before creating the claimable.</small>
          </>
        )}

        {recipientType === 'hash' && (
          <>
            <label htmlFor="recipientHash">Recipient hash (0x…)</label>
            <input
              id="recipientHash"
              placeholder="0x…"
              value={recipientHash}
              onChange={(event) => setRecipientHash(event.target.value)}
            />
          </>
        )}

        <label htmlFor="token">Token</label>
        <select id="token" value={token} onChange={(event) => setToken(event.target.value as 'NHB' | 'ZNHB')}>
          <option value="NHB">NHB</option>
          <option value="ZNHB">ZNHB</option>
        </select>

        <label htmlFor="amount">Amount</label>
        <input
          id="amount"
          placeholder="1.0"
          value={amount}
          onChange={(event) => setAmount(event.target.value)}
        />

        <label htmlFor="deadline">Expiry (hours from now)</label>
        <input
          id="deadline"
          type="number"
          min={1}
          value={deadlineHours}
          onChange={(event) => setDeadlineHours(Number(event.target.value))}
        />

        <button type="button" onClick={handleCreateClaimable}>Create claimable</button>
        {claimableStatus && (
          <div className="alert alert-success">
            Claimable {claimableStatus.claimId} for {formatNumber(claimableStatus.amount)} {claimableStatus.token} created. Expires {formatDate(claimableStatus.expiresAt)}.
          </div>
        )}
        {claimableError && <div className="alert alert-error">{claimableError}</div>}
      </section>

      <section>
        <h2>Claim funds</h2>
        <p>Redeem a claimable once the recipient controls the alias or email preimage.</p>
        <label htmlFor="claimId">Claim ID</label>
        <input
          id="claimId"
          placeholder="0x…"
          value={claimId}
          onChange={(event) => setClaimId(event.target.value)}
        />

        <label htmlFor="claimAlias">Alias (auto derives preimage)</label>
        <input
          id="claimAlias"
          placeholder="recipient"
          value={claimAlias}
          onChange={(event) => setClaimAlias(event.target.value)}
        />

        <label htmlFor="claimPreimage">Preimage override (0x…)</label>
        <input
          id="claimPreimage"
          placeholder="optional"
          value={claimPreimage}
          onChange={(event) => setClaimPreimage(event.target.value)}
        />

        <button type="button" onClick={handleClaim}>Claim</button>
        {claimStatus && <div className="alert alert-success">{claimStatus}</div>}
        {claimError && <div className="alert alert-error">{claimError}</div>}
      </section>

      <section>
        <h2>Escrow disputes</h2>
        <p>Review escrow details, surface the payee identity, and freeze the funds if a scam is suspected.</p>
        <label htmlFor="disputeEscrowId">Escrow ID</label>
        <input
          id="disputeEscrowId"
          placeholder="ESCROW..."
          value={disputeEscrowId}
          onChange={(event) => setDisputeEscrowId(event.target.value)}
        />
        <div className="form-footer">
          <button type="button" onClick={handleLoadEscrowDetails} disabled={disputeLoading}>
            {disputeLoading ? 'Loading escrow…' : 'Load escrow'}
          </button>
        </div>
        {disputeDetails && (
          <div className="card-list">
            <div className="card-list-item">
              <strong>Status</strong>
              <div>{disputeDetails.status}</div>
            </div>
            <div className="card-list-item">
              <strong>Token</strong>
              <div>
                {disputeDetails.token} · {formatNumber(disputeDetails.amount)}
              </div>
            </div>
            <div className="card-list-item">
              <strong>Payee address</strong>
              <div className="code-inline">{disputeDetails.payee}</div>
            </div>
            <div className="card-list-item">
              <strong>Payee identity</strong>
              <div>{disputeDetails.payeeAlias ? `@${disputeDetails.payeeAlias}` : '—'}</div>
            </div>
            {disputeDetails.payeeAliasId && (
              <div className="card-list-item">
                <strong>Alias ID</strong>
                <div className="code-inline">{disputeDetails.payeeAliasId}</div>
              </div>
            )}
          </div>
        )}
        <label htmlFor="disputeReason">Dispute reason (optional)</label>
        <textarea
          id="disputeReason"
          placeholder="Describe what went wrong…"
          value={disputeReason}
          onChange={(event) => setDisputeReason(event.target.value)}
          rows={3}
        />
        <label className="checkbox" htmlFor="markScamToggle" style={{ marginTop: '0.5rem' }}>
          <input
            id="markScamToggle"
            type="checkbox"
            checked={markAsScam}
            onChange={(event) => handleMarkAsScamToggle(event.target.checked)}
            disabled={!disputeDetails || disputeMarking}
          />
          <span>Mark this escrow as scam</span>
        </label>
        {disputeMarking && <p>Submitting dispute…</p>}
        {disputeSuccess && <div className="alert alert-success">{disputeSuccess}</div>}
        {disputeError && <div className="alert alert-error">{disputeError}</div>}
      </section>

      <section>
        <h2>QR payment intent</h2>
        <p>Share a <code className="code-inline">znhb://pay</code> URI that wallets can scan.</p>
        <label htmlFor="qrAlias">Alias</label>
        <input
          id="qrAlias"
          placeholder="merchant"
          value={recipientAlias}
          onChange={(event) => setRecipientAlias(event.target.value)}
        />
        <label htmlFor="qrAmount">Amount</label>
        <input
          id="qrAmount"
          placeholder="1.0"
          value={amount}
          onChange={(event) => setAmount(event.target.value)}
        />
        {paymentIntent ? (
          <div style={{ display: 'flex', gap: '1.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
            <QRCode value={paymentIntent} size={160} bgColor="#0b1120" fgColor="#e2e8f0" />
            <pre>{paymentIntent}</pre>
          </div>
        ) : (
          <p>Enter an alias to generate a QR code.</p>
        )}
      </section>
    </div>
  );
}
