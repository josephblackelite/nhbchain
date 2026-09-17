import type { AppProps } from 'next/app';
import Head from 'next/head';
import Layout from '../components/Layout';
import '../styles/globals.css';

export default function MyApp({ Component, pageProps }: AppProps) {
  return (
    <>
      <Head>
        <title>NHBChain Lending Example</title>
        <meta name="description" content="Reference implementation for the NHBChain lending flows." />
      </Head>
      <Layout>
        <Component {...pageProps} />
      </Layout>
    </>
  );
}
