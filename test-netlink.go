package main

import (
	"fmt"
	"github.com/vishvananda/netlink"
)

func main() {
	var h *netlink.Handle
	fmt.Printf("%T\n", h.LinkSetHardwareAddr)
}
