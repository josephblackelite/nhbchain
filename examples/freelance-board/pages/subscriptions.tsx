import Head from 'next/head';
import Link from 'next/link';

export default function Subscriptions() {
  return (
    <>
      <Head>
        <title>Subscription journey</title>
      </Head>
      <main>
        <section>
          <h1>Time-boxed subscription</h1>
          <p>
            Retainers are modelled as recurring milestone legs. The optional subscription payload is toggled via
            <code>escrow_milestoneSubscriptionUpdate</code> which flips the active flag while preserving historical release data.
          </p>
          <Link href="/">Back to board</Link>
        </section>

        <section>
          <h2>Workflow</h2>
          <ol>
            <li>Project owner drafts a subscription leg with the next release timestamp and billing interval.</li>
            <li>Payer funds the leg each cycle, typically via an automated scheduler that tracks `NextReleaseAt`.</li>
            <li>When deliverables are confirmed the payee triggers `escrow_milestoneRelease`.</li>
            <li>If the relationship pauses the payer deactivates the subscription which halts further reminders.</li>
          </ol>
        </section>

        <section>
          <h2>Guard rails</h2>
          <ul>
            <li>Deadlines always roll forward; the UI nudges the payer when the due event fires.</li>
            <li>Use governance policies to cap how far ahead subscriptions may be funded.</li>
            <li>Wallets should snapshot each cycle&apos;s invoice metadata so the eventual engine can reconcile payouts deterministically.</li>
          </ul>
        </section>
      </main>
    </>
  );
}
