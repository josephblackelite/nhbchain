import { z } from 'zod';

const serverConfigSchema = z.object({
  rpcUrl: z.string().url(),
  rpcToken: z.string().min(1),
  chainId: z.string().min(1),
  wsUrl: z.string().url().optional(),
  appBaseUrl: z.string().url().optional()
});

const clientConfigSchema = z.object({
  chainId: z.string().optional(),
  wsUrl: z.string().optional()
});

export type ServerConfig = z.infer<typeof serverConfigSchema>;
export type ClientConfig = z.infer<typeof clientConfigSchema>;

export function readServerConfig(): ServerConfig {
  const parsed = serverConfigSchema.safeParse({
    rpcUrl: process.env.NHB_RPC_URL,
    rpcToken: process.env.NHB_RPC_TOKEN,
    chainId: process.env.NHB_CHAIN_ID,
    wsUrl: process.env.NHB_WS_URL,
    appBaseUrl: process.env.APP_PUBLIC_BASE
  });
  if (!parsed.success) {
    const issues = parsed.error.issues.map((issue) => `${issue.path.join('.') || 'unknown'}: ${issue.message}`).join(', ');
    throw new Error(`P2P Mini-Market configuration invalid: ${issues}`);
  }
  return parsed.data;
}

export function readClientConfig(): ClientConfig {
  const parsed = clientConfigSchema.safeParse({
    chainId: process.env.NEXT_PUBLIC_NHB_CHAIN_ID ?? process.env.NHB_CHAIN_ID,
    wsUrl: process.env.NEXT_PUBLIC_NHB_WS_URL ?? process.env.NHB_WS_URL
  });
  if (!parsed.success) {
    throw new Error('Invalid client configuration');
  }
  return parsed.data;
}
