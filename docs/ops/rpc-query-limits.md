# Public RPC query limits

Most read methods answer in microseconds. A few of them scan blocks, or hold the
node's exclusive state lock while they work, and their cost grows with the
chain. On a small validator (two cores) that competes directly with block
production. This page describes how the node bounds them and which knobs exist.
The defaults need no configuration.

## What is limited

A query takes a slot in a small pool only for the heavy part of its work.
Queries that the indexes and caches can answer never wait for a slot.

| Method | Heavy part |
| --- | --- |
| `nhb_getTransaction`, `nhb_getTransactionReceipt` | a scan of the last 50,000 blocks, only reached when the transaction-hash index is incomplete or stale; a receipt also holds the state lock for its simulation |
| `nhb_searchExplorer`, `nhb_getTransactionHistory`, `nhb_getAddressActivity` | reading blocks that the per-block summaries say hold user-facing transactions |
| `nhb_getExplorerSnapshot` | computing a window other than the default one (cached per window and chain height) |
| `nhb_getLatestTransactions`, `nhb_txWindowStats` | reading blocks that are not yet summarised |
| `lending_getMarket`, `lend_getPools`, `lending_getUserAccount`, `market_listOpenListings`, `market_getMyListings`, `market_getMyFills` | reading a set that grows with the chain (the open listings, a fill history, the lending history of a pool) from a copy of the state |
| `swap_voucher_list`, `swap_voucher_export` | reading a page (list) or a time range (export) of the voucher ledger from a copy of the state (these and `swap_voucher_get` need a partner signature or, when partner authentication is not configured, a bearer token or client certificate) |

A query is charged for each block it has to read. Up to 8 blocks are free; past
that it must hold a slot. The methods that read state take a slot before they
read, and receipts take one before they take the state lock for their
simulation.

Reads of a set that grows with the chain are made from a copy of the state, or,
for the event lists, from a view of the event log. The node takes the state lock
only for as long as the copy takes and releases it before the read, so the time
such a read needs is CPU time, not time that block validation and commit wait.
The copy itself takes time in proportion to the part of the state trie that has
been loaded or changed since the last block was committed, not to the size of the
state. This covers the methods above, and also `potso_leaderboard`, `potso_top`,
`potso_getWeight`, `potso_epoch_payouts`, `potso_rewards_history`,
`potso_export_epoch`, the subscription list methods and the event-backed methods
(`nhb_getSlashingEvents`, `escrow_listEvents`, `nhb_getOwnerWalletStats`), which
are not in the pool.

Every one of these methods also runs under a deadline, and stops reading as soon
as its client disconnects.

## The pool

| Setting (`config.toml`) | Default | Meaning |
| --- | --- | --- |
| `RPCQueryMaxConcurrent` | half the CPUs, at least 1, at most 2 (1 on two cores) | queries running their heavy part at once |
| `RPCQueryMaxPerClient` | 1 | queries one client may have in flight, running or waiting |
| `RPCQueryQueueDepth` | 8 | queries allowed to wait for a slot |
| `RPCQueryQueueWaitMS` | 100 | how long one waits before it is refused |
| `RPCQueryTimeoutSeconds` | 10 | bound on one such query, start to answer |

The global limit is kept below the CPU count so that block production,
validation and commit always have a core of their own. Raise it only on hosts
with spare cores.

A key that many callers share (see "Rate-limit keys" below) may fill the queue
as well as the pool, so a local service is not refused because it shares an
address with other local traffic.

## What a client sees

* **Pool full or client over its limit.** HTTP 429, JSON-RPC code `-32020` (the
  rate-limit code), a `Retry-After` header in seconds and error data
  `{"reason": "...", "retryAfterMs": N}`. `reason` is `client_limit`,
  `queue_full` or `wait_timeout`. The query is refused before it does further
  work; a refused query has read at most 8 blocks.
* **Out of time.** HTTP 503, JSON-RPC code `-32021`, `Retry-After: 1`.
* **Lending index behind the chain.** Only just after a restart, and only for a
  lending read that has to rebuild a pool or an account from the committed lending
  transactions (nothing is stored for it): HTTP 503, JSON-RPC code `-32000`,
  `Retry-After: 1` and a message that says the lending index is catching up with
  the chain. A lending read that the stored state answers is not affected.
* **Client gone.** The request ends with status 499 and nothing is written.

Successful responses are unchanged. Clients should treat `-32020` as they
already do: back off and retry after `Retry-After`.

Refusals are counted in `nhb_module_throttles_total{reason="query_capacity"}` and
`nhb_rpc_limiter_hits_total{scope="query_<reason>"}`.

## Indexes and caches behind the limits

* **Transaction-hash index.** `AddBlock` indexes every transaction; the start-up
  backfill covers blocks stored before the index existed and records that it
  finished. When it has, a hash the index does not know is in no block and is
  answered without reading any. On a store whose backfill did not finish the node
  falls back to the bounded scan, under the pool.
* **Block summaries.** For each recent block the node keeps its timestamp, its
  transaction count and whether it holds a user-facing transaction (8 bytes per
  block, bounded to the most recent 2,000,000). Address history, the snapshot
  look-back, transaction-window counts and the latest transactions read only the
  blocks the summaries point at. The summaries are derived from committed blocks
  and are rebuilt after a restart as queries need them. A block that could not be
  read is remembered as unreadable for a few seconds only, then read again, so a
  one-off store read error is put right within seconds.
* **Snapshots.** Any window other than the default is computed once per chain
  height and shared; at most 8 windows are kept. `nhb_txWindowStats` keeps at most
  32 results.
* **Block digests.** A rebuild of the explorer snapshot keeps what it took from
  each block (its records and statistics; never the tip block, whose state root
  an operator recovery can rewrite) in a ring of 1,024 blocks. A rebuild after a
  new block reads that block and the one that stopped being the tip, not the
  window again, and the look-back over older blocks costs no reads at all once
  those blocks have been seen. A block with more than 128 such records is read
  again rather than kept, which holds the ring to a few tens of megabytes at
  most. The snapshot is the same as before, field for field.
* **Lending replay.** The lending reads rebuild a pool from the committed lending
  transactions. Each committed lending transaction is decoded once, the first time
  its block is looked at (pool, kind, signer, value; about 100 bytes of memory
  each, growing with the number of committed lending transactions and not
  capped), and the events are kept per pool. A pool's rebuild is the sum of its
  events, kept and brought forward over the blocks committed since the last read,
  so a read needs no block read, JSON decoding or signature recovery and costs
  time in the new blocks only. The sum is taken before the state lock, so the
  rebuild no longer runs inside it. What a read still does inside the lock is
  read the stored state and, as before, make the pass over the accounts that
  migrates positions recorded the old way for a pool that has not completed that
  pass yet; that pass is not changed here and its cost grows with the number of
  accounts. A pool that no committed lending transaction names costs nothing and
  keeps nothing in the index. A background task looks at the blocks after start-up
  at a quarter of one core; a read that arrives before it has finished helps for
  at most 100 ms and, if it needs the index and the index is still behind, answers
  503 (see above). A block that could not be read when it was looked at is not
  taken for one with no lending transaction: it is read again a few seconds later,
  and again every few seconds for as long as it stays unreadable.

## The explorer loop

Unless told otherwise the node rebuilds the default explorer snapshot in the
background after every block, streams it to `/ws/explorer` and advances the
all-time payment index (2,000 blocks a step, one block a step once caught up).
The rebuild used to read up to 50,000 blocks each time; it now costs a handful of
reads (see above). On a node that serves no public RPC it can be switched off:

    RPCDisableExplorerLoop = true

With it set no rebuild runs in the background, the lending read index is not
warmed (the lending reads that need it advance it themselves, 100 ms at a time,
and answer 503 until it has caught up), and `/ws/explorer` answers 503. A
snapshot that is asked for is still built (once per window and chain height,
under the pool) and moves the payment index on by one step when it does. The
default, `false`, keeps today's behaviour.

## Rate-limit keys

The rate limits and the pool apply to a key per request:

* A client address relayed by a listed proxy (`RPCTrustedProxies`) must be an IP
  address. Anything else in `X-Forwarded-For` or `X-Real-IP` is refused with
  HTTP 403 instead of becoming a bucket of its own.
* An IPv6 address is limited as its /64 network.
* A request that comes from a listed proxy address with no client address (a local
  service calling the node directly, or a proxied request the proxy did not
  attribute) shares the proxy address as its key. If it presents a valid bearer
  token, the token's `sub` claim is its key instead (`svc/<sub>`), so a local
  service that authenticates has a quota of its own that anonymous traffic cannot
  use up. Local services should send their bearer token on read calls too.

Put a reverse proxy in front of the node so that it sets `X-Forwarded-For` to the
address it saw, replacing any value the client sent; a client-supplied value that
the proxy passes through can still name a different address each time.

## Sizing

The pool bounds work done inside the node. It does not bound the number of
requests that reach it: cheap rejections still cost a little CPU each, so
volumetric floods have to be stopped in front of the node, as before.
