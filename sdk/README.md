# NHB Chain Go SDK

The Go SDK provides typed clients for interacting with NHB Chain services such as
consensus and network daemons. By default, all SDK gRPC clients attempt to
establish TLS connections using the host operating system's certificate pool to
mirror production security settings.

## Network client defaults

The `sdk/network` client now mirrors the consensus client behaviour by choosing
TLS transport credentials when no explicit dial options are supplied. Local
development that relies on plaintext gRPC endpoints must opt in by passing
`network.WithInsecure()` (or the shared `dial.WithInsecure()` helper) when
calling `network.Dial`.

Explicitly specifying TLS material via `WithTLSConfig`, `WithTLSFromFiles`, or
`WithSystemCertPool` continues to be supported.
