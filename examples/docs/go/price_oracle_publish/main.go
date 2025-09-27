package main

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "os"
        "time"

        "google.golang.org/grpc"
        "google.golang.org/grpc/credentials/insecure"

        networkv1 "nhbchain/proto/network/v1"
)

func main() {
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()

        endpoint := os.Getenv("ORACLE_GRPC_ADDR")
        if endpoint == "" {
                endpoint = "localhost:9555"
        }

        conn, err := grpc.DialContext(ctx, endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
        if err != nil {
                log.Fatalf("dial price oracle: %v", err)
        }
        defer conn.Close()

        client := networkv1.NewNetworkServiceClient(conn)
        stream, err := client.Gossip(ctx)
        if err != nil {
                log.Fatalf("open gossip stream: %v", err)
        }
        defer stream.CloseSend()

        payload, err := json.Marshal(map[string]any{
                "symbol":     "NHB/USD",
                "price":      "1.0002",
                "timestamp":  time.Now().UTC().Format(time.RFC3339),
                "publisherId": "oracle-publisher-1",
        })
        if err != nil {
                log.Fatalf("marshal price payload: %v", err)
        }

        msg := &networkv1.GossipRequest{
                Envelope: &networkv1.NetworkEnvelope{
                        Event: &networkv1.NetworkEnvelope_Gossip{
                                Gossip: &networkv1.GossipMessage{
                                        Type:    7001,
                                        Payload: payload,
                                },
                        },
                },
        }
        if err := stream.Send(msg); err != nil {
                log.Fatalf("send price gossip: %v", err)
        }

        ack, err := stream.Recv()
        if err != nil {
                log.Fatalf("receive oracle ack: %v", err)
        }
        ackEnvelope := ack.GetEnvelope()
        if ackEnvelope == nil {
                fmt.Println("oracle acknowledged price update with empty envelope")
                return
        }
        if gossip := ackEnvelope.GetGossip(); gossip != nil {
                fmt.Printf("oracle acknowledged price update: %s\n", string(gossip.GetPayload()))
        } else {
                fmt.Println("oracle acknowledged price update with empty payload")
        }
}
