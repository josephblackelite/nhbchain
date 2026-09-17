import { NextRequest, NextResponse } from 'next/server';
import { readServerConfig } from '../../../lib/config';
import { rpcRequest } from '../../../lib/rpc';

const DEFAULT_GATEWAY_BASE = 'https://gw.nhbcoin.net';

interface GatewayContentItem {
  id?: string;
  title?: string;
  uri?: string;
  tippedAt?: string;
  publishedAt?: string;
}

export async function GET(req: NextRequest) {
  const alias = req.nextUrl.searchParams.get('alias');
  if (!alias) {
    return NextResponse.json({ error: 'alias parameter required' }, { status: 400 });
  }

  try {
    const identity = await rpcRequest<{
      alias: string;
      aliasId: string;
      primary: string;
      addresses: string[];
      avatarRef?: string;
      createdAt: number;
      updatedAt: number;
    }>('identity_resolve', [alias]);

    const serverConfig = readServerConfig();
    const gatewayBase = (process.env.NHB_API_URL || DEFAULT_GATEWAY_BASE).replace(/\/$/, '');
    const appBase = (serverConfig.appBaseUrl || gatewayBase).replace(/\/$/, '');

    let recentContent: GatewayContentItem[] = [];

    try {
      const response = await fetch(
        `${gatewayBase}/creator/v1/content?alias=${encodeURIComponent(alias)}`,
        { cache: 'no-store' }
      );
      if (response.ok) {
        const data = (await response.json()) as { data?: GatewayContentItem[] };
        if (Array.isArray(data?.data)) {
          recentContent = data.data.slice(0, 5);
        }
      }
    } catch (error) {
      console.warn('Failed to load creator content from gateway', error);
    }

    if (recentContent.length === 0) {
      recentContent = [
        {
          id: 'demo-drop',
          title: `Sample drop from @${alias}`,
          uri: `${appBase}/creators/${encodeURIComponent(alias)}/demo`,
        },
      ];
    }

    const avatarUrl = identity.avatarRef
      ? `${appBase}/${identity.avatarRef.replace(/^\//, '')}`
      : null;

    return NextResponse.json(
      {
        alias: identity.alias,
        primary: identity.primary,
        addresses: identity.addresses,
        avatarUrl,
        createdAt: identity.createdAt,
        updatedAt: identity.updatedAt,
        recentContent,
      },
      { status: 200 }
    );
  } catch (error) {
    return NextResponse.json({ error: (error as Error).message }, { status: 404 });
  }
}
