'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { z } from 'zod';
import { CreateBusinessForm } from './components/CreateBusinessForm';
import { ProgramForm } from './components/ProgramForm';
import { ProgramCard } from './components/ProgramCard';
import { RpcError, formatAmount, rpcCall } from './lib/rpc';
import type { BusinessResult, ProgramResult, ProgramStats } from './types';

interface AlertState {
  type: 'success' | 'error';
  message: string;
}

const businessIdSchema = z
  .string()
  .min(1, 'Business ID is required')
  .regex(/^0x[0-9a-fA-F]{64}$/, 'Business ID must be 32-byte hex (0x-prefixed).');

const addressSchema = z
  .string()
  .min(1, 'Address is required')
  .regex(/^nhb1[0-9a-z]+$/i, 'Expected a valid nhb Bech32 address');

const isoDay = () => new Date().toISOString().slice(0, 10);

export default function MerchantLoyaltyConsole() {
  const [adminAddress, setAdminAddress] = useState('');
  const [businessIdInput, setBusinessIdInput] = useState('');
  const [business, setBusiness] = useState<BusinessResult | null>(null);
  const [programs, setPrograms] = useState<ProgramResult[]>([]);
  const [paymasterBalance, setPaymasterBalance] = useState('0');
  const [statsDay, setStatsDay] = useState(() => isoDay());
  const [statsByProgram, setStatsByProgram] = useState<Record<string, ProgramStats>>({});
  const [alert, setAlert] = useState<AlertState | null>(null);
  const [loadingBusiness, setLoadingBusiness] = useState(false);
  const [paymasterInput, setPaymasterInput] = useState('');
  const [paymasterSubmitting, setPaymasterSubmitting] = useState(false);
  const [newMerchant, setNewMerchant] = useState('');
  const [merchantSubmitting, setMerchantSubmitting] = useState(false);
  const [polling, setPolling] = useState(false);

  const programsRef = useRef<ProgramResult[]>([]);
  programsRef.current = programs;

  useEffect(() => {
    if (typeof window === 'undefined') return;
    const storedAdmin = window.localStorage.getItem('mlc-admin');
    const storedBusiness = window.localStorage.getItem('mlc-business');
    if (storedAdmin) setAdminAddress(storedAdmin);
    if (storedBusiness) {
      setBusinessIdInput(storedBusiness);
    }
  }, []);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    if (adminAddress) {
      window.localStorage.setItem('mlc-admin', adminAddress);
    }
  }, [adminAddress]);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    if (business?.id) {
      window.localStorage.setItem('mlc-business', business.id);
    }
  }, [business?.id]);

  const handleLoadBusiness = async () => {
    setAlert(null);
    const parsed = businessIdSchema.safeParse(businessIdInput.trim());
    if (!parsed.success) {
      setAlert({ type: 'error', message: parsed.error.issues[0]?.message || 'Invalid business ID' });
      return;
    }
    setLoadingBusiness(true);
    try {
      const businessResult = await rpcCall<BusinessResult>('loyalty_getBusiness', [
        { businessId: parsed.data }
      ]);
      setBusiness(businessResult);
      setPaymasterInput(businessResult.paymaster || '');
      await refreshPrograms(parsed.data);
      await refreshPaymaster(parsed.data);
      setAlert({ type: 'success', message: `Loaded business ${businessResult.name}` });
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'Failed to load business';
      setAlert({ type: 'error', message });
      setBusiness(null);
      setPrograms([]);
    } finally {
      setLoadingBusiness(false);
    }
  };

  const refreshPrograms = async (id: string) => {
    try {
      const list = await rpcCall<ProgramResult[]>('loyalty_listPrograms', [
        { businessId: id }
      ]);
      setPrograms(list);
      return list;
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'Failed to list programs';
      setAlert({ type: 'error', message });
      return [] as ProgramResult[];
    }
  };

  const refreshPaymaster = async (id: string) => {
    try {
      const balance = await rpcCall<string>('loyalty_paymasterBalance', [
        { businessId: id }
      ]);
      setPaymasterBalance(balance ?? '0');
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'Failed to fetch paymaster balance';
      setAlert({ type: 'error', message });
    }
  };

  const refreshStats = async (programId: string, day: string) => {
    try {
      const result = await rpcCall<ProgramStats>('loyalty_programStats', [
        { programId, day }
      ]);
      setStatsByProgram((prev) => ({ ...prev, [programId]: result }));
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'Failed to fetch program stats';
      setAlert({ type: 'error', message });
    }
  };

  useEffect(() => {
    if (!business?.id) return;
    if (programs.length === 0) return;
    programs.forEach((program) => {
      void refreshStats(program.id, statsDay);
    });
  }, [statsDay, programs, business?.id]);

  useEffect(() => {
    if (!polling || !business?.id) return;
    const interval = window.setInterval(async () => {
      const list = await refreshPrograms(business.id);
      const nextPrograms = list.length > 0 ? list : programsRef.current;
      nextPrograms.forEach((program) => {
        void refreshStats(program.id, statsDay);
      });
      await refreshPaymaster(business.id);
    }, 15000);
    return () => window.clearInterval(interval);
  }, [polling, business?.id, statsDay]);

  const handleSetPaymaster = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!business?.id) return;
    setAlert(null);
    const parsed = addressSchema.safeParse(paymasterInput.trim());
    if (!parsed.success) {
      setAlert({ type: 'error', message: parsed.error.issues[0]?.message || 'Invalid paymaster address' });
      return;
    }
    if (!adminAddress) {
      setAlert({ type: 'error', message: 'Set an admin caller address before rotating the paymaster' });
      return;
    }
    setPaymasterSubmitting(true);
    try {
      await rpcCall<string>('loyalty_setPaymaster', [
        { caller: adminAddress.trim(), businessId: business.id, paymaster: parsed.data }
      ]);
      setAlert({ type: 'success', message: 'Paymaster rotated successfully' });
      setBusiness((prev) => (prev ? { ...prev, paymaster: parsed.data } : prev));
      await refreshPaymaster(business.id);
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'Failed to set paymaster';
      setAlert({ type: 'error', message });
    } finally {
      setPaymasterSubmitting(false);
    }
  };

  const handleAddMerchant = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!business?.id) return;
    const parsed = addressSchema.safeParse(newMerchant.trim());
    if (!parsed.success) {
      setAlert({ type: 'error', message: parsed.error.issues[0]?.message || 'Invalid merchant address' });
      return;
    }
    if (!adminAddress) {
      setAlert({ type: 'error', message: 'Set an admin caller address before adding merchants' });
      return;
    }
    setMerchantSubmitting(true);
    try {
      await rpcCall<string>('loyalty_addMerchant', [
        { caller: adminAddress.trim(), businessId: business.id, merchant: parsed.data }
      ]);
      setAlert({ type: 'success', message: 'Merchant added successfully' });
      setNewMerchant('');
      const updated = await rpcCall<BusinessResult>('loyalty_getBusiness', [
        { businessId: business.id }
      ]);
      setBusiness(updated);
      await refreshPrograms(business.id);
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'Failed to add merchant';
      setAlert({ type: 'error', message });
    } finally {
      setMerchantSubmitting(false);
    }
  };

  const handleRemoveMerchant = async (merchant: string) => {
    if (!business?.id) return;
    if (!adminAddress) {
      setAlert({ type: 'error', message: 'Set an admin caller address before removing merchants' });
      return;
    }
    try {
      await rpcCall<string>('loyalty_removeMerchant', [
        { caller: adminAddress.trim(), businessId: business.id, merchant }
      ]);
      const updated = await rpcCall<BusinessResult>('loyalty_getBusiness', [
        { businessId: business.id }
      ]);
      setBusiness(updated);
      await refreshPrograms(business.id);
      setAlert({ type: 'success', message: 'Merchant removed successfully' });
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'Failed to remove merchant';
      setAlert({ type: 'error', message });
    }
  };

  const handleProgramLifecycle = async (programId: string, action: 'pause' | 'resume') => {
    if (!adminAddress) {
      setAlert({ type: 'error', message: 'Set an admin caller address before updating programs' });
      return;
    }
    const method = action === 'pause' ? 'loyalty_pauseProgram' : 'loyalty_resumeProgram';
    try {
      await rpcCall<string>(method, [
        { caller: adminAddress.trim(), programId }
      ]);
      if (business?.id) {
        await refreshPrograms(business.id);
      }
      setAlert({ type: 'success', message: `Program ${action}d successfully` });
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'Failed to update program status';
      setAlert({ type: 'error', message });
    }
  };

  const activePrograms = useMemo(() => programs.filter((program) => program.active), [programs]);
  const pausedPrograms = useMemo(() => programs.filter((program) => !program.active), [programs]);

  return (
    <>
      <header>
        <h1>Merchant Loyalty Console</h1>
        <p className="lede">
          Configure NHBChain loyalty businesses, rotate paymasters, and orchestrate program accruals that pay out ZNHB on escrow
          settlement. RPC requests are proxied via <code>https://api.nhbcoin.net</code> with optional bearer authentication.
        </p>
      </header>

      <section>
        <h2>Environment</h2>
        <div className="grid columns-2">
          <div>
            <label htmlFor="admin-address">Admin / caller wallet</label>
            <input
              id="admin-address"
              value={adminAddress}
              onChange={(event) => setAdminAddress(event.target.value)}
              placeholder="nhb1..."
              autoComplete="off"
            />
            <small>RPC calls that mutate state use this wallet as the authorised caller.</small>
          </div>
          <div>
            <label htmlFor="business-id">Business ID</label>
            <input
              id="business-id"
              value={businessIdInput}
              onChange={(event) => setBusinessIdInput(event.target.value)}
              placeholder="0x..."
              autoComplete="off"
            />
            <div className="form-footer">
              <button type="button" onClick={handleLoadBusiness} disabled={loadingBusiness}>
                {loadingBusiness ? 'Loading…' : 'Load business'}
              </button>
              {business?.name ? (
                <small>
                  Loaded: <span className="code-inline">{business.name}</span>
                </small>
              ) : null}
            </div>
          </div>
        </div>
        {alert ? <div className={`alert alert-${alert.type}`}>{alert.message}</div> : null}
      </section>

      <CreateBusinessForm defaultCaller={adminAddress} onCreated={(id) => setBusinessIdInput(id)} />

      {business ? (
        <section>
          <h2>Business overview</h2>
          <div className="grid columns-2">
            <div>
              <p className="badge">Business ID</p>
              <p className="code-inline">{business.id}</p>
            </div>
            <div>
              <p className="badge">Owner</p>
              <p className="code-inline">{business.owner}</p>
            </div>
          </div>
          <div className="grid columns-2">
            <div>
              <p className="badge">Paymaster pool</p>
              <p className="code-inline">{business.paymaster || 'Not set'}</p>
            </div>
            <div>
              <p className="badge">Paymaster balance (ZNHB wei)</p>
              <p className="code-inline">{formatAmount(paymasterBalance)}</p>
            </div>
          </div>
        </section>
      ) : null}

      {business ? (
        <section>
          <h2>Rotate paymaster</h2>
          <form onSubmit={handleSetPaymaster} className="grid columns-2">
            <div>
              <label htmlFor="paymaster-input">New paymaster address</label>
              <input
                id="paymaster-input"
                value={paymasterInput}
                onChange={(event) => setPaymasterInput(event.target.value)}
                placeholder="nhb1..."
              />
            </div>
            <div className="form-footer">
              <button type="submit" disabled={paymasterSubmitting}>
                {paymasterSubmitting ? 'Updating…' : 'Set paymaster'}
              </button>
              <small>
                Caller: <span className="code-inline">{adminAddress || 'Set admin wallet'}</span>
              </small>
            </div>
          </form>
        </section>
      ) : null}

      {business ? (
        <section>
          <h2>Merchants</h2>
          <form onSubmit={handleAddMerchant} className="grid columns-2">
            <div>
              <label htmlFor="merchant-input">Add merchant</label>
              <input
                id="merchant-input"
                value={newMerchant}
                onChange={(event) => setNewMerchant(event.target.value)}
                placeholder="nhb1..."
              />
            </div>
            <div className="form-footer">
              <button type="submit" disabled={merchantSubmitting}>
                {merchantSubmitting ? 'Adding…' : 'Add merchant'}
              </button>
              <small>Merchants inherit this business&apos;s loyalty programs.</small>
            </div>
          </form>
          <div className="tag-list">
            {business.merchants.length === 0 ? <span className="tag">No merchants registered</span> : null}
            {business.merchants.map((merchant) => (
              <span key={merchant} className="tag">
                {merchant}{' '}
                <button
                  type="button"
                  className="secondary"
                  style={{ padding: '0.2rem 0.6rem', fontSize: '0.7rem' }}
                  onClick={() => handleRemoveMerchant(merchant)}
                >
                  Remove
                </button>
              </span>
            ))}
          </div>
        </section>
      ) : null}

      {business ? (
        <ProgramForm
          businessId={business.id}
          caller={adminAddress.trim()}
          defaultOwner={business.merchants[0] || ''}
          defaultPool={business.paymaster || ''}
          onCompleted={async () => {
            await refreshPrograms(business.id);
            await refreshPaymaster(business.id);
          }}
        />
      ) : null}

      {business ? (
        <section>
          <h2>Loyalty programs</h2>
          <div className="form-footer" style={{ marginBottom: '1rem' }}>
            <div>
              <label htmlFor="stats-day">Stats day (UTC)</label>
              <input
                id="stats-day"
                type="date"
                value={statsDay}
                onChange={(event) => setStatsDay(event.target.value)}
              />
            </div>
            <div>
              <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <input
                  type="checkbox"
                  checked={polling}
                  onChange={(event) => setPolling(event.target.checked)}
                />
                Auto-refresh every 15s
              </label>
            </div>
          </div>
          <div className="card-list">
            {[...activePrograms, ...pausedPrograms].map((program) => (
              <ProgramCard
                key={`${program.id}-${program.active}-${program.accrualBps}-${program.startTime}-${program.endTime}`}
                program={program}
                stats={statsByProgram[program.id]}
                caller={adminAddress.trim()}
                businessId={business.id}
                onPause={(id) => handleProgramLifecycle(id, 'pause')}
                onResume={(id) => handleProgramLifecycle(id, 'resume')}
                onRefresh={(id) => refreshStats(id, statsDay)}
                onUpdated={async () => {
                  await refreshPrograms(business.id);
                  await refreshPaymaster(business.id);
                  await refreshStats(program.id, statsDay);
                }}
              />
            ))}
            {programs.length === 0 ? <div className="tag">No programs yet — create one above.</div> : null}
          </div>
        </section>
      ) : null}
    </>
  );
}
