'use client';

import { useState } from 'react';
import { z } from 'zod';
import { rpcCall, RpcError } from '../lib/rpc';

const schema = z.object({
  caller: z.string().min(1, 'Caller address is required'),
  owner: z.string().optional(),
  name: z.string().min(3, 'Business name must contain at least 3 characters')
});

export interface CreateBusinessFormProps {
  defaultCaller?: string;
  onCreated?: (businessId: string, owner: string) => void;
}

export function CreateBusinessForm({ defaultCaller = '', onCreated }: CreateBusinessFormProps) {
  const [form, setForm] = useState({ caller: defaultCaller, owner: '', name: '' });
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    setResult(null);

    const parsed = schema.safeParse(form);
    if (!parsed.success) {
      setSubmitting(false);
      setError(parsed.error.issues[0]?.message || 'Invalid form values');
      return;
    }

    const payload = {
      caller: parsed.data.caller.trim(),
      owner: parsed.data.owner?.trim() || parsed.data.caller.trim(),
      name: parsed.data.name.trim()
    };

    try {
      const businessId = await rpcCall<string>('loyalty_createBusiness', [payload]);
      setResult(businessId);
      if (onCreated) {
        onCreated(businessId, payload.owner);
      }
    } catch (err) {
      const message = err instanceof RpcError ? err.message : 'Failed to create business';
      setError(message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <section>
      <h2>Register a new business</h2>
      <p className="lede">
        Creates an on-chain business profile that can own merchants and loyalty programs. The caller must be authorised on the
        NHB RPC endpoint.
      </p>
      <form onSubmit={handleSubmit} className="grid">
        <div>
          <label htmlFor="create-caller">Caller wallet</label>
          <input
            id="create-caller"
            value={form.caller}
            onChange={(event) => setForm((prev) => ({ ...prev, caller: event.target.value }))}
            placeholder="nhb1..."
            autoComplete="off"
          />
        </div>
        <div>
          <label htmlFor="create-owner">Owner wallet (optional)</label>
          <input
            id="create-owner"
            value={form.owner}
            onChange={(event) => setForm((prev) => ({ ...prev, owner: event.target.value }))}
            placeholder="Defaults to caller if blank"
            autoComplete="off"
          />
        </div>
        <div>
          <label htmlFor="create-name">Business name</label>
          <input
            id="create-name"
            value={form.name}
            onChange={(event) => setForm((prev) => ({ ...prev, name: event.target.value }))}
            placeholder="e.g. Zenith Coffee Group"
            autoComplete="off"
          />
        </div>
        <div className="form-footer">
          <button type="submit" disabled={submitting}>
            {submitting ? 'Creating…' : 'Create business'}
          </button>
          {result ? <small>Business ID: <span className="code-inline">{result}</span></small> : null}
        </div>
        {error ? <div className="alert alert-error">{error}</div> : null}
        {result ? (
          <div className="alert alert-success">
            Business created successfully. Use the returned ID to configure merchants and loyalty programs.
          </div>
        ) : null}
      </form>
    </section>
  );
}
