import { credentials } from "@grpc/grpc-js";
import { MsgClient } from "../../clients/ts/gov/v1/tx";

async function main() {
  const client = new MsgClient("localhost:50061", credentials.createInsecure());
  const proposer = "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpkkh94"; // replace with signer address

  client.submitProposal(
    {
      proposer,
      title: "Update staking minimum",
      description: JSON.stringify({
        target: "param.update",
        payload: {
          "staking.minimumValidatorStake": "1500000000000000000000",
        },
      }),
      deposit: "250000000000000000000",
    },
    (err, resp) => {
      if (err) {
        console.error("submitProposal error", err);
        return;
      }
      console.log("broadcasted proposal tx", resp.txHash);
    }
  );
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
