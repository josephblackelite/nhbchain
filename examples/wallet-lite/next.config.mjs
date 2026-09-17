const nextConfig = {
  reactStrictMode: true,
  experimental: {
    serverActions: {
      allowedOrigins: [process.env.APP_PUBLIC_BASE || 'http://localhost:3000']
    }
  }
};

export default nextConfig;
