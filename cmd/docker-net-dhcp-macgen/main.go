package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/thehaven/docker-net-dhcp/pkg/macgen"
)

func main() {
	format := flag.String("format", "colon", "MAC address format (colon, hyphen, dot)")
	duid := flag.Bool("duid", false, "Generate DUID-LL instead of MAC address")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <seed>\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}

	seed := flag.Arg(0)

	var macFormat macgen.Format
	switch *format {
	case "colon":
		macFormat = macgen.FormatColon
	case "hyphen":
		macFormat = macgen.FormatHyphen
	case "dot":
		macFormat = macgen.FormatDot
	default:
		fmt.Fprintf(os.Stderr, "Invalid format: %s. Must be 'colon', 'hyphen', or 'dot'.\n", *format)
		os.Exit(1)
	}

	mac, err := macgen.Generate(macgen.Options{
		Seed:   seed,
		Format: macFormat,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating MAC: %v\n", err)
		os.Exit(1)
	}

	if *duid {
		duidStr, err := macgen.GenerateDUID(mac)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating DUID: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(duidStr)
	} else {
		fmt.Println(mac)
	}
}
