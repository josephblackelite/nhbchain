# Lending gRPC client (TypeScript)

This example shows how to call the `lending.v1.LendingService` from TypeScript.  The repository ships
Node-oriented stubs generated with [`ts-proto`](https://github.com/stephenh/ts-proto) under
[`clients/ts/lending/v1`](../../../clients/ts/lending/v1).  The stubs target `@grpc/grpc-js`, so they
can be used directly from Node.js services or from tools that support the standard gRPC protocol.

## Prerequisites

- Node.js 18+
- `npm install @grpc/grpc-js @bufbuild/protobuf`
- Access to a running lendingd gRPC endpoint (default `localhost:9090`)

## Minimal Node usage

```ts
import { credentials } from "@grpc/grpc-js";
import { LendingServiceClient, ListMarketsRequest } from "nhbchain/clients/ts/lending/v1/lending";

const client = new LendingServiceClient(
  "localhost:9090",
  credentials.createInsecure(), // use createSsl for TLS-enabled endpoints
);

const request: ListMarketsRequest = {};
client.listMarkets(request, (err, response) => {
  if (err) {
    console.error("listMarkets failed", err);
    return;
  }
  console.log("markets", response?.markets);
});
```

When connecting to a TLS-enabled endpoint replace `createInsecure()` with `credentials.createSsl()`
and provide the certificate chain issued by the gateway.

## Browser usage

Browsers cannot speak HTTP/2 gRPC directly.  To call the service from the browser you have two
options:

1. **grpc-web proxy** – Run a sidecar such as [`improbable-eng/grpcwebproxy`](https://github.com/improbable-eng/grpcwebproxy)
   or Envoy with the [gRPC-Web filter](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/grpc_web_filter)
   to translate between gRPC-Web and standard gRPC.
2. **Custom gateway** – Expose the required RPCs over REST or WebSockets and forward them to the gRPC
   service, reusing the same protobuf messages.

If you choose the grpc-web proxy approach, point your web application at the proxy address (e.g.
`http://localhost:8080`) and configure it to forward requests to `localhost:9090`.  Many libraries,
including [`@bufbuild/connect-web`](https://connect.build/docs/web/clients/), can generate browser
clients that speak the grpc-web protocol; reuse the protobuf schema in `proto/lending/v1/lending.proto`
when generating those bindings.

## Authentication

Production deployments typically require per-RPC metadata for authentication.  With `@grpc/grpc-js`
you can attach metadata using the optional `Metadata` argument:

```ts
import { Metadata } from "@grpc/grpc-js";

const metadata = new Metadata();
metadata.add("authorization", "Bearer <token>");
client.getPosition({ account: "nhb1..." }, metadata, (err, response) => {
  /* ... */
});
```

Ensure your proxy (if any) forwards those headers to avoid `UNAUTHENTICATED` responses.
