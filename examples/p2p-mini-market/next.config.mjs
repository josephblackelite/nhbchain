import nextConfig from '../wallet-lite/next.config.mjs';

export default {
  ...nextConfig,
  experimental: {
    ...nextConfig.experimental,
  }
};
