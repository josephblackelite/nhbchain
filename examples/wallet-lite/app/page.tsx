import dynamic from 'next/dynamic';

const WalletLiteApp = dynamic(() => import('./components/wallet-lite-app'), {
  ssr: false
});

export default function Page() {
  return <WalletLiteApp />;
}
