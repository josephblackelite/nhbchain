import { NextResponse } from 'next/server';
import { readClientConfig } from '../../lib/config';

export async function GET() {
  const config = readClientConfig();
  return NextResponse.json(config, { status: 200 });
}
