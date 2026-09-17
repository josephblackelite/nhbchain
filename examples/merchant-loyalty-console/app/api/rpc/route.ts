import { NextRequest, NextResponse } from 'next/server';

const DEFAULT_RPC = 'https://api.nhbcoin.net/rpc';

const rpcUrl = (process.env.NHB_RPC_URL || DEFAULT_RPC).trim().replace(/\/$/, '');
const rpcToken = process.env.NHB_RPC_TOKEN?.trim();

// NHB-AUDIT-C5: this handler used to forward ANY client-specified method
// to the node with the server's own privileged bearer token attached
// (or not, per a client-controlled `useAuth` flag in the SAME request
// body deciding whether ITS OWN privileged call gets authenticated) --
// no allowlist, no visitor authentication. Only the exact methods this
// UI calls (see app/page.tsx, app/components/*.tsx) are allowed through;
// everything else is rejected before ever reaching the node. Token
// attachment is now decided ONLY by this server-side classification,
// never by anything in the request body.
//
// READ_ONLY_METHODS are safe to expose to any anonymous visitor (pure
// queries, no state change). MUTATING_METHODS move funds, create/modify
// a business or program, or add/remove a merchant on behalf of an
// admin/caller address supplied in the request body -- none of them are
// verified server-side against the actual visitor, since this demo has
// no session/login/signature-verification system at all. Deploying this
// beyond a local/trusted-network demo requires adding real per-visitor
// authentication (e.g. a wallet-signature challenge) before any mutating
// call is allowed, not just this allowlist.
const READ_ONLY_METHODS = new Set([
  'loyalty_getBusiness',
  'loyalty_listPrograms',
  'loyalty_paymasterBalance',
  'loyalty_programStats',
  'loyalty_getCreatorRewardsPool',
  'loyalty_creatorRewardsStats',
]);
const MUTATING_METHODS = new Set([
  'loyalty_setPaymaster',
  'loyalty_addMerchant',
  'loyalty_removeMerchant',
  'loyalty_setCreatorRewardsPool',
  'loyalty_createProgram',
  'loyalty_updateProgram',
  'loyalty_createBusiness',
  'loyalty_pauseProgram',
  'loyalty_resumeProgram',
]);

export async function POST(req: NextRequest) {
  try {
    const body = await req.json();
    const { method, params, id } = body ?? {};

    if (typeof method !== 'string' || method.length === 0) {
      return NextResponse.json(
        { error: { message: 'RPC method is required' } },
        { status: 400 }
      );
    }
    if (!READ_ONLY_METHODS.has(method) && !MUTATING_METHODS.has(method)) {
      return NextResponse.json(
        { error: { message: `method not allowed: ${method}` } },
        { status: 403 }
      );
    }

    const payload = {
      jsonrpc: '2.0',
      id: typeof id === 'number' || typeof id === 'string' ? id : Date.now(),
      method,
      params: Array.isArray(params) ? params : params ? [params] : []
    };

    const headers: Record<string, string> = {
      'Content-Type': 'application/json'
    };
    if (rpcToken && MUTATING_METHODS.has(method)) {
      headers.Authorization = `Bearer ${rpcToken}`;
    }

    const response = await fetch(rpcUrl, {
      method: 'POST',
      headers,
      body: JSON.stringify(payload),
      cache: 'no-store'
    });

    const text = await response.text();
    let data: unknown;
    try {
      data = text ? JSON.parse(text) : {};
    } catch (error) {
      return NextResponse.json(
        {
          error: {
            message: 'Failed to decode RPC response',
            detail: (error as Error).message,
            raw: text
          }
        },
        { status: 502 }
      );
    }

    if (!response.ok) {
      return NextResponse.json(data, { status: response.status });
    }

    return NextResponse.json(data);
  } catch (error) {
    return NextResponse.json(
      {
        error: {
          message: 'RPC proxy error',
          detail: error instanceof Error ? error.message : String(error)
        }
      },
      { status: 500 }
    );
  }
}
