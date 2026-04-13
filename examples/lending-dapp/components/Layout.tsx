import Link from 'next/link';
import { useRouter } from 'next/router';
import { ReactNode } from 'react';

const navLinks = [
  { href: '/', label: 'Overview' },
  { href: '/earn', label: 'Earn' },
  { href: '/borrow', label: 'Borrow' }
];

export default function Layout({ children }: { children: ReactNode }) {
  const router = useRouter();

  return (
    <>
      <nav className="navbar">
        <Link href="/">
          <strong>NHBChain Lending</strong>
        </Link>
        <div className="nav-links">
          {navLinks.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              className={`nav-link${router.pathname === link.href ? ' active' : ''}`}
            >
              {link.label}
            </Link>
          ))}
        </div>
      </nav>
      <main>{children}</main>
    </>
  );
}
