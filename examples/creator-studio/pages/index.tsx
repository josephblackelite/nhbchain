import { FormEvent, useMemo, useState } from 'react';

const fetcher = async (method: string, params: unknown[]) => {
  const response = await fetch('/api/rpc', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ method, params }),
  });
  const json = await response.json();
  if (!response.ok || json.error) {
    throw new Error(json?.error?.message ?? json?.error ?? 'RPC error');
  }
  return json.result;
};

type LedgerState = {
  pending: string;
  totalTips: string;
  totalYield: string;
  lastPayout: number;
  claimed?: string;
};

type ContentResult = {
  id: string;
  creator: string;
  uri: string;
  metadata: string;
  publishedAt: number;
  totalTips: string;
  totalStake: string;
};

// creator_publish, creator_tip, creator_stake and creator_payouts' claim:true
// path are disabled server-side (410 Gone): they used to mutate
// validator-local state outside the block pipeline, which guarantees a
// consensus fork/halt on this chain's 2-validator zero-quorum-slack
// topology, and no signed-transaction replacement exists yet (see
// rpc/creator_handlers.go's creatorRPCDisabledMessage). The buttons below
// are disabled so this UI never shows a working-looking control for a flow
// that cannot succeed.
const RETIRED_NOTE =
  'Retired: this method is disabled server-side (410 Gone) until a signed-transaction replacement ships.';

export default function CreatorStudio() {
  const [creator, setCreator] = useState('');
  const [fan, setFan] = useState('');
  const [contentId, setContentId] = useState('demo-drop');
  const [uri, setUri] = useState('ipfs://cid');
  const [metadata, setMetadata] = useState('{"title":"Demo Drop"}');
  const [tipAmount, setTipAmount] = useState('100000000000000000');
  const [stakeAmount, setStakeAmount] = useState('500000000000000000');
  const [statusLog, setStatusLog] = useState<string[]>([]);
  const [content, setContent] = useState<ContentResult | null>(null);
  const [ledger, setLedger] = useState<LedgerState | null>(null);

  const log = (entry: string) => {
    setStatusLog((prev) => [new Date().toLocaleTimeString(), entry, ...prev]);
  };

  const call = async (method: string, params: unknown[]) => {
    try {
      const result = await fetcher(method, params);
      log(`${method} → success`);
      return result;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      log(`${method} → ${message}`);
      throw error;
    }
  };

  const handlePublish = (event: FormEvent) => {
    event.preventDefault();
    log(`creator_publish → ${RETIRED_NOTE}`);
  };

  const handleTip = (event: FormEvent) => {
    event.preventDefault();
    log(`creator_tip → ${RETIRED_NOTE}`);
  };

  const handleStake = (event: FormEvent) => {
    event.preventDefault();
    log(`creator_stake → ${RETIRED_NOTE}`);
  };

  const handlePayouts = async (claim: boolean) => {
    if (claim) {
      log(`creator_payouts (claim) → ${RETIRED_NOTE}`);
      return;
    }
    if (!creator) {
      log('creator address required');
      return;
    }
    try {
      const result = (await call('creator_payouts', [
        {
          caller: creator,
          claim,
        },
      ])) as LedgerState;
      setLedger(result);
    } catch (error) {
      console.error(error);
    }
  };

  const prettyLedger = useMemo(() => {
    if (!ledger) {
      return 'no payouts yet';
    }
    const lines = [
      `Pending: ${ledger.pending ?? '0'} wei`,
      `Total Tips: ${ledger.totalTips ?? '0'} wei`,
      `Total Yield: ${ledger.totalYield ?? '0'} wei`,
    ];
    if (ledger.lastPayout) {
      lines.push(`Last Payout: ${new Date(ledger.lastPayout * 1000).toLocaleString()}`);
    }
    if (ledger.claimed) {
      lines.push(`Claimed: ${ledger.claimed} wei`);
    }
    return lines.join('\n');
  }, [ledger]);

  return (
    <main>
      <header style={{ marginBottom: '2rem' }}>
        <span className="badge">Creator Studio Demo</span>
        <h1>Publish → Tip → Stake → Payout</h1>
        <p>
          Use devnet addresses to exercise the full lifecycle. The app proxies JSON-RPC calls through
          <code> /api/rpc</code> so you only need to provide the correct bearer token in <code>.env.local</code>.
        </p>
      </header>

      <section className="card">
        <h2>Actor Setup</h2>
        <div className="grid">
          <div>
            <label>
              Creator Address
              <input value={creator} onChange={(event) => setCreator(event.target.value.trim())} placeholder="nhb1..." />
            </label>
          </div>
          <div>
            <label>
              Fan Address
              <input value={fan} onChange={(event) => setFan(event.target.value.trim())} placeholder="nhb1..." />
            </label>
          </div>
        </div>
      </section>

      <section className="card">
        <h2>1. Publish Content</h2>
        <form onSubmit={handlePublish} className="grid">
          <label>
            Content ID
            <input value={contentId} onChange={(event) => setContentId(event.target.value)} disabled />
          </label>
          <label>
            Content URI
            <input value={uri} onChange={(event) => setUri(event.target.value)} disabled />
          </label>
          <label style={{ gridColumn: 'span 2' }}>
            Metadata JSON
            <textarea rows={3} value={metadata} onChange={(event) => setMetadata(event.target.value)} disabled />
          </label>
          <button type="submit" disabled title={RETIRED_NOTE}>
            Publish (retired)
          </button>
        </form>
        <p style={{ marginTop: '1rem' }}>{RETIRED_NOTE}</p>
        {content && (
          <p style={{ marginTop: '1rem' }}>
            Latest content <strong>{content.id}</strong> published at{' '}
            {new Date(content.publishedAt * 1000).toLocaleString()}
          </p>
        )}
      </section>

      <section className="card">
        <h2>2. Tip the Drop</h2>
        <form onSubmit={handleTip} className="grid">
          <label>
            Amount (wei)
            <input value={tipAmount} onChange={(event) => setTipAmount(event.target.value)} disabled />
          </label>
          <button type="submit" disabled title={RETIRED_NOTE}>
            Send Tip (retired)
          </button>
        </form>
        <p style={{ marginTop: '1rem' }}>{RETIRED_NOTE}</p>
      </section>

      <section className="card">
        <h2>3. Stake Behind the Creator</h2>
        <form onSubmit={handleStake} className="grid">
          <label>
            Amount (wei)
            <input value={stakeAmount} onChange={(event) => setStakeAmount(event.target.value)} disabled />
          </label>
          <button type="submit" disabled title={RETIRED_NOTE}>
            Stake (retired)
          </button>
        </form>
        <p style={{ marginTop: '1rem' }}>{RETIRED_NOTE}</p>
      </section>

      <section className="card">
        <h2>4. Payouts</h2>
        <div className="grid">
          <button type="button" onClick={() => handlePayouts(false)}>
            Refresh Ledger
          </button>
          <button type="button" disabled title={RETIRED_NOTE} onClick={() => handlePayouts(true)}>
            Claim Pending (retired)
          </button>
        </div>
        <p style={{ marginTop: '1rem' }}>Claiming pending payouts is retired. {RETIRED_NOTE}</p>
        <div className="log" style={{ marginTop: '1.5rem' }}>
          {prettyLedger}
        </div>
      </section>

      <section className="card">
        <h2>Activity Log</h2>
        <div className="log">
          {statusLog.length === 0 ? 'no activity yet' : statusLog.join('\n')}
        </div>
      </section>
    </main>
  );
}
