import dynamic from 'next/dynamic';

const MiniMarketApp = dynamic(() => import('./components/mini-market-app'), {
  ssr: false
});

export default function Page() {
  return <MiniMarketApp />;
}
