#!/bin/bash
set -e

NETWORK="vlan107"
NAME="test-deterministic-mac"
IMAGE="alpine"

echo "--- Phase 1: Cleaning up environment ---"
sudo docker rm -f $NAME >/dev/null 2>&1 || true

echo "--- Phase 2: First Attempt ---"
MAC1=$(sudo docker run --rm --network $NETWORK --name $NAME $IMAGE ip link show eth0 | grep link/ether | awk '{print $2}')
echo "Attempt 1 MAC: $MAC1"

# Brief sleep to ensure Docker daemon and plugin have settled
sleep 2

echo "--- Phase 3: Second Attempt ---"
MAC2=$(sudo docker run --rm --network $NETWORK --name $NAME $IMAGE ip link show eth0 | grep link/ether | awk '{print $2}')
echo "Attempt 2 MAC: $MAC2"

echo "--- Phase 4: Validation ---"
if [ "$MAC1" == "$MAC2" ]; then
    echo "SUCCESS: MAC address is static across attempts ($MAC1)"
    exit 0
else
    echo "FAILURE: MAC address changed!"
    echo "MAC 1: $MAC1"
    echo "MAC 2: $MAC2"
    exit 1
fi
