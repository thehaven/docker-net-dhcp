package main

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

func main() {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		panic(err)
	}

	ctrs, err := cli.ContainerList(context.Background(), container.ListOptions{All: true})
	if err != nil {
		panic(err)
	}

	for _, c := range ctrs {
		ctr, err := cli.ContainerInspect(context.Background(), c.ID)
		if err == nil && ctr.NetworkSettings != nil && ctr.NetworkSettings.SandboxKey != "" {
			fmt.Printf("Container: %s, SandboxKey: %s\n", c.Names[0], ctr.NetworkSettings.SandboxKey)
		}
	}
}
