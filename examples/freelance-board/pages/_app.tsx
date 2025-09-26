import type { AppProps } from 'next/app';
import '../styles.css';

export default function FreelanceBoardApp({ Component, pageProps }: AppProps) {
  return <Component {...pageProps} />;
}
