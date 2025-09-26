'use client';

import { useEffect, useMemo, useState } from 'react';
import { z } from 'zod';
import { rpcCall, RpcError, toUnixSeconds, fromUnixSeconds } from '../lib/rpc';
import type { ProgramResult } from '../types';

const draftSchema = z.object({
  id: z
    .string()
    .min(1, 'Program ID is required')
    .regex(/^0x[0-9a-fA-F]{64}$/, 'Program ID must be 32-byte hex (0x-prefixed).'),
  owner: z.string().min(1, 'Owner address is required'),
  pool: z.string().min(1, 'Paymaster pool address is required'),
  tokenSymbol: z.string().min(1, 'Token symbol is required'),
  accrualBps: z
    .string()
    .min(1, 'Accrual BPS is required')
    .refine((value) => /^\d+$/.test(value), 'Accrual BPS must be a non-negative integer'),
  minSpendWei: z.string().optional(),
  capPerTx: z.string().optional(),
  dailyCapUser: z.string().optional(),
  startTime: z.string().optional(),
  endTime: z.string().optional(),
  active: z.boolean()
});

export interface ProgramDraft {
  id: string;
  owner: string;
  pool: string;
  tokenSymbol: string;
  accrualBps: string;
  minSpendWei: string;
  capPerTx: string;
  dailyCapUser: string;
  startTime: string;
  endTime: string;
  active: boolean;
}

export interface ProgramFormProps {
  businessId: string;
  caller: string;
  defaultOwner?: string;
  defaultPool?: string;
  mode?: 'create' | 'update';
  initialDraft?: ProgramDraft;
  onCompleted?: (programId?: string) => void;
  renderSection?: boolean;
  heading?: string;
}

const emptyDraft: ProgramDraft = {
  id: '',
  owner: '',
  pool: '',
  tokenSymbol: 'ZNHB',
  accrualBps: '500',
  minSpendWei: '0',
  capPerTx: '0',
  dailyCapUser: '0',
  startTime: '',
  endTime: '',
  active: true
};

export function ProgramForm({
  businessId,
  caller,
  defaultOwner,
  defaultPool,
  mode = 'create',
  initialDraft,
  onCompleted,
  renderSection = true,
  heading
}: ProgramFormProps) {
  const [draft, setDraft] = useState<ProgramDraft>(() => {
    if (initialDraft) {
      return initialDraft;
    }
    return {
      ...emptyDraft,
      owner: defaultOwner || '',
      pool: defaultPool || ''
    };
  });
  useEffect(() => {
    if (initialDraft) {
      setDraft(initialDraft);
    }
  }, [initialDraft]);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  const title = heading || (mode === 'create' ? 'Create loyalty program' : 'Update program');
  const submitLabel = mode === 'create' ? 'Create program' : 'Update program';

  const showScheduleFields = useMemo(() => draft.startTime !== '' || draft.endTime !== '', [draft.startTime, draft.endTime]);

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    setSuccess(null);

    const parsed = draftSchema.safeParse({ ...draft, active: draft.active });
    if (!parsed.success) {
      setSubmitting(false);
      setError(parsed.error.issues[0]?.message || 'Invalid program configuration');
      return;
    }

    const data = parsed.data;
    const spec: Record<string, unknown> = {
      id: data.id,
      owner: data.owner,
      pool: data.pool,
      tokenSymbol: data.tokenSymbol.toUpperCase(),
      accrualBps: Number(data.accrualBps),
      minSpendWei: (data.minSpendWei && data.minSpendWei.trim() !== '' ? data.minSpendWei : '0'),
      capPerTx: (data.capPerTx && data.capPerTx.trim() !== '' ? data.capPerTx : '0'),
      dailyCapUser: (data.dailyCapUser && data.dailyCapUser.trim() !== '' ? data.dailyCapUser : '0'),
      active: data.active
    };

    const start = toUnixSeconds(data.startTime || '');
    const end = toUnixSeconds(data.endTime || '');
    if (start !== undefined) {
      spec.startTime = start;
    }
    if (end !== undefined) {
      spec.endTime = end;
    }

    const envelope = mode === 'create'
      ? { caller, businessId, spec }
      : { caller, spec };

    try {
      if (mode === 'create') {
        const programId = await rpcCall<string>('loyalty_createProgram', [envelope]);
        setSuccess(`Program created: ${programId}`);
        if (onCompleted) onCompleted(programId);
        setDraft((prev) => ({ ...prev, id: '', owner: defaultOwner || prev.owner, pool: defaultPool || prev.pool }));
      } else {
        await rpcCall<string>('loyalty_updateProgram', [envelope]);
        setSuccess('Program updated successfully');
        if (onCompleted) onCompleted(draft.id);
      }
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'RPC request failed';
      setError(message);
    } finally {
      setSubmitting(false);
    }
  };

  const handleGenerateId = () => {
    const bytes = new Uint8Array(32);
    if (typeof window !== 'undefined' && window.crypto && window.crypto.getRandomValues) {
      window.crypto.getRandomValues(bytes);
    } else {
      for (let i = 0; i < bytes.length; i += 1) {
        bytes[i] = Math.floor(Math.random() * 256);
      }
    }
    const hex = Array.from(bytes)
      .map((b) => b.toString(16).padStart(2, '0'))
      .join('');
    setDraft((prev) => ({ ...prev, id: `0x${hex}` }));
  };

  const form = (
    <form onSubmit={handleSubmit} className="grid">
        <div className="grid columns-2">
          <div>
            <label htmlFor="program-id">Program ID</label>
            <div className="stack-sm">
              <input
                id="program-id"
                value={draft.id}
                onChange={(event) => setDraft((prev) => ({ ...prev, id: event.target.value }))}
                placeholder="0x...32-byte identifier"
                autoComplete="off"
              />
              {mode === 'create' ? (
                <button type="button" className="secondary" onClick={handleGenerateId}>
                  Generate ID
                </button>
              ) : null}
            </div>
          </div>
          <div>
            <label htmlFor="program-owner">Program owner (merchant)</label>
            <input
              id="program-owner"
              value={draft.owner}
              onChange={(event) => setDraft((prev) => ({ ...prev, owner: event.target.value }))}
              placeholder="nhb1..."
              autoComplete="off"
            />
          </div>
        </div>
        <div className="grid columns-2">
          <div>
            <label htmlFor="program-pool">Paymaster pool</label>
            <input
              id="program-pool"
              value={draft.pool}
              onChange={(event) => setDraft((prev) => ({ ...prev, pool: event.target.value }))}
              placeholder="nhb1..."
              autoComplete="off"
            />
          </div>
          <div>
            <label htmlFor="program-token">Token symbol</label>
            <input
              id="program-token"
              value={draft.tokenSymbol}
              onChange={(event) => setDraft((prev) => ({ ...prev, tokenSymbol: event.target.value }))}
              placeholder="ZNHB"
              autoComplete="off"
            />
          </div>
        </div>
        <div className="grid columns-2">
          <div>
            <label htmlFor="program-bps">Accrual (basis points)</label>
            <input
              id="program-bps"
              value={draft.accrualBps}
              onChange={(event) => setDraft((prev) => ({ ...prev, accrualBps: event.target.value }))}
              placeholder="e.g. 500 for 5%"
            />
          </div>
          <div>
            <label htmlFor="program-active">Program status</label>
            <div>
              <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <input
                  type="checkbox"
                  checked={draft.active}
                  onChange={(event) => setDraft((prev) => ({ ...prev, active: event.target.checked }))}
                />
                Active
              </label>
            </div>
          </div>
        </div>
        <div className="grid columns-2">
          <div>
            <label htmlFor="program-min">Minimum spend (wei)</label>
            <input
              id="program-min"
              value={draft.minSpendWei}
              onChange={(event) => setDraft((prev) => ({ ...prev, minSpendWei: event.target.value }))}
            />
          </div>
          <div>
            <label htmlFor="program-cap">Cap per transaction (wei)</label>
            <input
              id="program-cap"
              value={draft.capPerTx}
              onChange={(event) => setDraft((prev) => ({ ...prev, capPerTx: event.target.value }))}
            />
          </div>
        </div>
        <div className="grid columns-2">
          <div>
            <label htmlFor="program-daily">Daily cap per user (wei)</label>
            <input
              id="program-daily"
              value={draft.dailyCapUser}
              onChange={(event) => setDraft((prev) => ({ ...prev, dailyCapUser: event.target.value }))}
            />
          </div>
          <div>
            <label htmlFor="program-start">Start time (UTC)</label>
            <input
              id="program-start"
              type="datetime-local"
              value={draft.startTime}
              onChange={(event) => setDraft((prev) => ({ ...prev, startTime: event.target.value }))}
            />
          </div>
        </div>
        <div className="grid columns-2">
          <div>
            <label htmlFor="program-end">End time (UTC)</label>
            <input
              id="program-end"
              type="datetime-local"
              value={draft.endTime}
              onChange={(event) => setDraft((prev) => ({ ...prev, endTime: event.target.value }))}
            />
          </div>
          {showScheduleFields ? (
            <div className="alert alert-success">
              Program schedule will be applied. Leave blank to keep the program always-on.
            </div>
          ) : (
            <div />
          )}
        </div>
        <div className="form-footer">
          <button type="submit" disabled={submitting || !caller || !businessId}>
            {submitting ? 'Submitting…' : submitLabel}
          </button>
          <small>
            Caller: <span className="code-inline">{caller || 'Set an admin wallet above'}</span>
          </small>
        </div>
        {error ? <div className="alert alert-error">{error}</div> : null}
        {success ? <div className="alert alert-success">{success}</div> : null}
      </form>
  );

  if (!renderSection) {
    return (
      <div className="stack-sm">
        <h3 style={{ margin: 0 }}>{title}</h3>
        {form}
      </div>
    );
  }

  return (
    <section>
      <h2>{title}</h2>
      {form}
    </section>
  );
}

export function createDraftFromProgram(program: ProgramResult): ProgramDraft {
  return {
    id: program.id,
    owner: program.owner,
    pool: program.pool,
    tokenSymbol: program.tokenSymbol,
    accrualBps: program.accrualBps.toString(),
    minSpendWei: program.minSpendWei || '0',
    capPerTx: program.capPerTx || '0',
    dailyCapUser: program.dailyCapUser || '0',
    startTime: fromUnixSeconds(program.startTime),
    endTime: fromUnixSeconds(program.endTime),
    active: program.active
  };
}
