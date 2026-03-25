#!/bin/bash
# deploy.sh
set -e

HOST="golemshost"
BIN_NAME="ann"
REMOTE_BIN="/usr/local/bin/$BIN_NAME"
SERVICE_NAME="ann.service"

echo "Building for Linux (amd64)..."
GOOS=linux GOARCH=amd64 go build -o $BIN_NAME .

echo "Stopping remote service..."
ssh -t $HOST "sudo systemctl stop $SERVICE_NAME"

echo "Uploading binary to $HOST..."
scp $BIN_NAME $HOST:/tmp/$BIN_NAME

echo "Installing and starting service..."
ssh -t $HOST "sudo mv /tmp/$BIN_NAME $REMOTE_BIN && \
              sudo chmod +x $REMOTE_BIN && \
              sudo systemctl start $SERVICE_NAME"

echo "Cleaning up local binary..."
rm $BIN_NAME

echo "Deployment complete! Check logs with:"
echo "   ssh -t $HOST 'sudo journalctl -fu $SERVICE_NAME'"