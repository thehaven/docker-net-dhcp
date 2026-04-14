#!/bin/bash
# Monitor container creation and check if sandbox key is visible before start
docker events --filter 'event=create' --filter 'event=start' --filter 'event=network connect' --format '{{.Status}} {{.id}}' | while read event id; do
    echo "Event: $event $id"
    docker inspect $id --format 'SandboxKey: {{.NetworkSettings.SandboxKey}}'
done
