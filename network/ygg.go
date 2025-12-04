package network

import (
	"log"

	ygg "github.com/svanichkin/Ygg"
)

// SetupYgg initializes an embedded Yggdrasil node using the provided configuration.
// It sets verbosity, peer limits, and connectivity callbacks before constructing the node.
// Returns the created node, its mesh address as string, or an error on failure.
func SetupYgg(verbose bool, cfgPath string, h func(connected bool)) (*ygg.Node, string, error) {
	log.Printf("[net] loading")
	ygg.SetVerbose(verbose)
	ygg.SetMaxPeers(100)
	ygg.SetConnectivityHandler(h)

	// create or load the Yggdrasil node using the supplied config path
	node, err := ygg.New(cfgPath)
	if err != nil {
		return nil, "", err
	}

	// obtain the node's mesh IPv6 address as a stable string representation
	addrStr := node.Core.Address().String()

	return node, addrStr, nil
}
