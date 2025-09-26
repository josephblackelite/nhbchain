package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

type authorityMetadata struct {
	Lookup string `json:"lookup"`
	TXT    string `json:"txt"`
	Domain string `json:"domain"`
}

func main() {
	var (
		authorityPath = flag.String("authority", "authority.json", "Path to authority JSON output from authority.go")
		listenAddr    = flag.String("listen", "127.0.0.1:8053", "Address to listen on (ip:port)")
		ttlSeconds    = flag.Int("ttl", 60, "TXT record TTL in seconds")
	)
	flag.Parse()

	meta, err := loadAuthority(*authorityPath)
	if err != nil {
		log.Fatalf("failed to load authority file: %v", err)
	}

	fqdn := dns.Fqdn(strings.TrimSpace(meta.Lookup))
	if fqdn == "." {
		log.Fatal("authority lookup name is empty")
	}
	txtValue := strings.TrimSpace(meta.TXT)
	if txtValue == "" {
		log.Fatal("authority TXT payload is empty")
	}

	handler := func(w dns.ResponseWriter, r *dns.Msg) {
		msg := &dns.Msg{}
		msg.SetReply(r)
		msg.Authoritative = true

		if len(r.Question) == 0 {
			_ = w.WriteMsg(msg)
			return
		}

		question := r.Question[0]
		name := strings.ToLower(question.Name)
		switch question.Qtype {
		case dns.TypeTXT:
			if name == strings.ToLower(fqdn) {
				rr := &dns.TXT{Hdr: dns.RR_Header{Name: fqdn, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: uint32(*ttlSeconds)}, Txt: []string{txtValue}}
				msg.Answer = append(msg.Answer, rr)
			} else {
				msg.Rcode = dns.RcodeNameError
			}
		default:
			msg.Rcode = dns.RcodeNotImplemented
		}

		if err := w.WriteMsg(msg); err != nil {
			log.Printf("failed to write DNS response: %v", err)
		}
	}

	dns.HandleFunc(".", handler)

	server := &dns.Server{Addr: *listenAddr, Net: "udp"}
	go func() {
		log.Printf("seed DNS stub listening on %s for %s", *listenAddr, fqdn)
		if err := server.ListenAndServe(); err != nil {
			log.Fatalf("dns server error: %v", err)
		}
	}()

	tcpServer := &dns.Server{Addr: *listenAddr, Net: "tcp"}
	go func() {
		if err := tcpServer.ListenAndServe(); err != nil {
			log.Fatalf("dns tcp server error: %v", err)
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = server.ShutdownContext(shutdownCtx)
	_ = tcpServer.ShutdownContext(shutdownCtx)
	log.Println("seed DNS stub shut down")
}

func loadAuthority(path string) (*authorityMetadata, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var meta authorityMetadata
	if err := json.Unmarshal(payload, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}
