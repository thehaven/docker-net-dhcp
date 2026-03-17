#!/bin/bash
set -e

DRIVER="ghcr.io/thehaven/docker-net-dhcp:golang"
NETWORK="vlan107"
IMAGE="alpine"
NAME="test-det-dns"
EXPECTED_MAC="02:9d:ef:ca:8f:1c" # Result of generate_mac test-det-dns (seed: test-det-dns)
DNS_SERVER="192.168.107.253"
DOMAIN="docker.thehavennet.org.uk"

echo "--- Phase 1: MAC Parity Test ---"
sudo docker rm -f $NAME >/dev/null 2>&1 || true
ACTUAL_MAC=$(sudo docker run --rm --network $NETWORK --name $NAME $IMAGE ip link show eth0 | grep link/ether | awk '{print $2}')

echo "Expected MAC: $EXPECTED_MAC"
echo "Actual MAC:   $ACTUAL_MAC"

if [ "$EXPECTED_MAC" != "$ACTUAL_MAC" ]; then
    echo "FAILURE: MAC address does not match generate_mac output!"
    exit 1
fi
echo "SUCCESS: MAC address parity achieved."

echo "--- Phase 2: DNS Registration Test ---"
# We need to run a background container to give udhcpc time to register
sudo docker run -d --network $NETWORK --name $NAME $IMAGE sleep 60
echo "Waiting 10s for DHCP/DNS propagation..."
sleep 10

RESOLVED_IP=$(dig +short @$DNS_SERVER ${NAME}.${DOMAIN})
echo "Resolved IP: $RESOLVED_IP"

if [ -z "$RESOLVED_IP" ]; then
    echo "FAILURE: DNS name ${NAME}.${DOMAIN} not registered!"
    sudo docker rm -f $NAME
    exit 1
fi

CONTAINER_IP=$(sudo docker exec $NAME ip addr show eth0 | grep "inet " | awk '{print $2}' | cut -d/ -f1)
echo "Container IP: $CONTAINER_IP"

if [ "$RESOLVED_IP" != "$CONTAINER_IP" ]; then
    echo "FAILURE: Resolved IP ($RESOLVED_IP) mismatch with Container IP ($CONTAINER_IP)!"
    sudo docker rm -f $NAME
    exit 1
fi

echo "SUCCESS: DHCP registered with DNS correctly."
sudo docker rm -f $NAME

echo "--- Final Validation Summary ---"
echo "All user stories (MAC Parity + DNS Registration) passed successfully."
exit 0
