'use client';

import { useState } from 'react';
import { ProgramForm, createDraftFromProgram } from './ProgramForm';
import type { ProgramResult, ProgramStats } from '../types';
import { formatAmount } from '../lib/rpc';

export interface ProgramCardProps {
  program: ProgramResult;
  stats?: ProgramStats;
  caller: string;
  businessId: string;
  onPause: (programId: string) => Promise<void> | void;
  onResume: (programId: string) => Promise<void> | void;
  onRefresh: (programId: string) => Promise<void> | void;
  onUpdated: () => void;
}

export function ProgramCard({
  program,
  stats,
  caller,
  businessId,
  onPause,
  onResume,
  onRefresh,
  onUpdated
}: ProgramCardProps) {
  const [showEditor, setShowEditor] = useState(false);
  const [lifecyclePending, setLifecyclePending] = useState(false);

  const handleLifecycle = async () => {
    setLifecyclePending(true);
    try {
      if (program.active) {
        await onPause(program.id);
      } else {
        await onResume(program.id);
      }
    } finally {
      setLifecyclePending(false);
    }
  };

  const handleRefresh = async () => {
    await onRefresh(program.id);
  };

  const scheduleSummary = () => {
    if (program.startTime === 0 && program.endTime === 0) {
      return 'Always on';
    }
    const format = (seconds: number) => {
      if (!seconds) return '—';
      return new Date(seconds * 1000).toISOString();
    };
    return `${format(program.startTime)} → ${format(program.endTime)}`;
  };

  const statsSummary = stats
    ? `${formatAmount(stats.rewardsPaid || '0')} wei paid • ${stats.txCount} settlements • cap usage ${formatAmount(
        stats.capUsage || '0'
      )}`
    : 'No stats available for selected day';

  return (
    <div className="card-list-item">
      <div className="form-footer" style={{ marginBottom: '1rem' }}>
        <div>
          <h3 style={{ margin: '0 0 0.35rem' }}>{program.tokenSymbol} loyalty</h3>
          <div className={`status-pill ${program.active ? 'active' : 'paused'}`}>
            {program.active ? 'Active' : 'Paused'}
          </div>
        </div>
        <div className="stack-sm" style={{ justifyItems: 'flex-end' }}>
          <button type="button" onClick={handleLifecycle} disabled={lifecyclePending}>
            {lifecyclePending ? 'Updating…' : program.active ? 'Pause program' : 'Resume program'}
          </button>
          <button type="button" className="secondary" onClick={handleRefresh}>
            Refresh stats
          </button>
          <button type="button" className="secondary" onClick={() => setShowEditor((prev) => !prev)}>
            {showEditor ? 'Hide editor' : 'Edit program'}
          </button>
        </div>
      </div>
      <div className="grid columns-2" style={{ marginBottom: '1rem' }}>
        <div>
          <p className="badge">Program ID</p>
          <p className="code-inline">{program.id}</p>
        </div>
        <div>
          <p className="badge">Owner (merchant)</p>
          <p className="code-inline">{program.owner}</p>
        </div>
      </div>
      <div className="grid columns-2" style={{ marginBottom: '1rem' }}>
        <div>
          <p className="badge">Paymaster pool</p>
          <p className="code-inline">{program.pool}</p>
        </div>
        <div>
          <p className="badge">Accrual rate</p>
          <p className="code-inline">{program.accrualBps} bps</p>
        </div>
      </div>
      <div className="grid columns-2" style={{ marginBottom: '1rem' }}>
        <div>
          <p className="badge">Caps</p>
          <p className="code-inline">
            Min spend {formatAmount(program.minSpendWei)} • Cap/tx {formatAmount(program.capPerTx)} • Daily user cap{' '}
            {formatAmount(program.dailyCapUser)}
          </p>
        </div>
        <div>
          <p className="badge">Schedule</p>
          <p className="code-inline">{scheduleSummary()}</p>
        </div>
      </div>
      <div className="alert alert-success" style={{ marginBottom: '1rem' }}>{statsSummary}</div>
      {showEditor ? (
        <ProgramForm
          businessId={businessId}
          caller={caller}
          mode="update"
          initialDraft={createDraftFromProgram(program)}
          renderSection={false}
          heading="Update program"
          onCompleted={() => {
            setShowEditor(false);
            onUpdated();
          }}
        />
      ) : null}
    </div>
  );
}
