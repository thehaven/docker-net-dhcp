package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

func main() {
	cli, _ := client.NewClientWithOpts(client.FromEnv)
	sandboxKey := "/var/run/docker/netns/88a533005042" // Using a known sandbox key from the logs

	ctrs, _ := cli.ContainerList(context.Background(), container.ListOptions{All: true})
	for _, c := range ctrs {
		ctr, err := cli.ContainerInspect(context.Background(), c.ID)
		if err == nil && ctr.NetworkSettings != nil && ctr.NetworkSettings.SandboxKey == sandboxKey {
			name := strings.TrimPrefix(c.Names[0], "/")
			fmt.Printf("Found container: %s\n", name)
			return
		}
	}
	fmt.Println("Not found")
}
