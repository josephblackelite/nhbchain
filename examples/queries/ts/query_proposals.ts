import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";

const endpoint = process.env.CONSENSUSD_GRPC_ADDR ?? "localhost:9090";
const proposalId = process.env.GOV_PROPOSAL_ID ?? "1";

async function main() {
  const definition = protoLoader.loadSync("proto/consensus/v1/query.proto", {
    keepCase: true,
    longs: String,
    enums: String,
    defaults: true,
    oneofs: true,
  });
  const pkg = grpc.loadPackageDefinition(definition) as any;
  const client = new pkg.consensus.v1.QueryService(
    endpoint,
    grpc.credentials.createInsecure()
  );

  client.QueryState(
    { namespace: "gov", key: `proposals/${proposalId}` },
    (err: grpc.ServiceError | null, resp: { value?: Buffer }) => {
      if (err) {
        console.error("query failed", err);
        process.exit(1);
      }
      if (!resp?.value || resp.value.length === 0) {
        console.log("proposal not found");
        process.exit(0);
      }
      try {
        const decoded = JSON.parse(resp.value.toString("utf8"));
        console.log(`proposal ${proposalId}`);
        console.log(JSON.stringify(decoded, null, 2));
      } catch (decodeErr) {
        console.error("failed to decode proposal payload", decodeErr);
        process.exit(1);
      }
    }
  );
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
