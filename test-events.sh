#!/bin/bash
docker events --filter 'event=create' --filter 'event=start' --filter 'event=network connect' --format '{{.Status}} {{.id}}' &
PID=$!
sudo docker restart mem0-mcp
sleep 5
kill $PID
