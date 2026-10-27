// Command devca gives our services certificates for development, from a new
// CA every time: its private key is gone the moment devca exits.
//
//	go run ./cmd/devca -dir secrets/tls gateway wallet transfer
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/fiston-user/payments/internal/platform/mtls/devca"
)

func main() {
	dir := flag.String("dir", "secrets/tls", "where each service gets a directory")
	validity := flag.Duration("valid", 7*24*time.Hour, "how long the certificates last")
	flag.Parse()
	ca, err := devca.New(*validity)
	if err != nil {
		log.Fatal(err)
	}
	for _, name := range flag.Args() {
		if err := ca.Write(*dir, name); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("%d certificates in %s, valid for %s\n", flag.NArg(), *dir, *validity)
}
