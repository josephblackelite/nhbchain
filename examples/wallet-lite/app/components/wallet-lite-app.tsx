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
