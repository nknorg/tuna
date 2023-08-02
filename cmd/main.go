package main

import (
	"errors"
	"log"
	"os"

	"github.com/jessevdk/go-flags"
)

var opts struct {
	BeneficiaryAddr   string `short:"b" long:"beneficiary-addr" description:"Beneficiary address (NKN wallet address to receive rewards)"`
	ServicesFile      string `short:"s" long:"services" description:"Services file path" default:"services.json"`
	WalletFile        string `short:"w" long:"wallet" description:"Wallet file path" default:"wallet.json"`
	PasswordFile      string `short:"p" long:"password-file" description:"Wallet password file path" default:"wallet.pswd"`
	SeedRPCServerAddr string `long:"rpc" description:"Seed RPC server address, separated by comma"`
	Version           bool   `short:"v" long:"version" description:"Print version"`
}

var (
	parser  = flags.NewParser(&opts, flags.Default)
	Version string
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Fatalf("Panic: %+v", r)
		}
	}()

	_, err := parser.Parse()
	if err != nil {
		var e *flags.Error
		if errors.As(err, &e) && e.Type == flags.ErrHelp {
			os.Exit(0)
		}
		log.Fatalln(err)
	}
}
