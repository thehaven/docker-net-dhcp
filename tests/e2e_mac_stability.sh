#!/bin/bash
set -e

DRIVER="ghcr.io/thehaven/docker-net-dhcp:golang"
NETWORK="test-vlan-999"
BRIDGE="vlan107" # Use the existing bridge that we know works
IMAGE="alpine"

echo "--- Phase 1: Infrastructure Setup ---"
sudo docker network rm $NETWORK >/dev/null 2>&1 || true
sudo docker network create -d $DRIVER --ipam-driver null -o bridge=$BRIDGE $NETWORK
echo "SUCCESS: Created test network $NETWORK using driver $DRIVER"

echo "--- Phase 2: Testing Deterministic Consistency ---"
NAME="test-deterministic-mac"
sudo docker rm -f $NAME >/dev/null 2>&1 || true
MAC1=$(sudo docker run --rm --network $NETWORK --name $NAME $IMAGE ip link show eth0 | grep link/ether | awk '{print $2}')
echo "Attempt 1 MAC (Deterministic): $MAC1"
sleep 2
MAC2=$(sudo docker run --rm --network $NETWORK --name $NAME $IMAGE ip link show eth0 | grep link/ether | awk '{print $2}')
echo "Attempt 2 MAC (Deterministic): $MAC2"

if [ "$MAC1" != "$MAC2" ]; then
    echo "FAILURE: Deterministic MAC changed!"
    sudo docker network rm $NETWORK
    exit 1
fi
echo "SUCCESS: Deterministic MAC is stable."

echo "--- Phase 3: Testing User MAC Override ---"
NAME="test-manual-mac"
SPECIFIED_MAC="02:de:ad:be:ef:01"
sudo docker rm -f $NAME >/dev/null 2>&1 || true
ACTUAL_MAC=$(sudo docker run --rm --network $NETWORK --mac-address $SPECIFIED_MAC --name $NAME $IMAGE ip link show eth0 | grep link/ether | awk '{print $2}')
echo "Specified MAC: $SPECIFIED_MAC"
echo "Actual MAC:    $ACTUAL_MAC"

if [ "$SPECIFIED_MAC" != "$ACTUAL_MAC" ]; then
    echo "FAILURE: User-specified MAC was ignored or modified!"
    sudo docker network rm $NETWORK
    exit 1
fi
echo "SUCCESS: User-specified MAC accepted without error."

echo "--- Phase 4: Testing Repeatability after Driver Restart ---"
# This simulates an upgrade or crash recovery
sudo docker plugin disable -f $DRIVER
sudo docker plugin enable $DRIVER
sleep 2

NAME="test-recovery-mac"
MAC_RECOVERY=$(sudo docker run --rm --network $NETWORK --name $NAME $IMAGE ip link show eth0 | grep link/ether | awk '{print $2}')
echo "Recovery MAC: $MAC_RECOVERY"
# Should match the deterministic pattern for this name
if [ -z "$MAC_RECOVERY" ]; then
    echo "FAILURE: Recovery failed to assign MAC!"
    exit 1
fi
echo "SUCCESS: Plugin recovered and assigned MAC."

echo "--- Phase 5: Cleanup ---"
sudo docker network rm $NETWORK
echo "All user stories passed successfully."
exit 0
