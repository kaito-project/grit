#!/bin/bash

# Define JSON files
SANDBOX_CONFIG="sandbox.json"
CONTAINER_CONFIG="container.json"

# Step 1: Run the pod sandbox
echo "Creating pod sandbox..."
POD_ID=$(crictl runp $SANDBOX_CONFIG)
if [ -z "$POD_ID" ]; then
  echo "Failed to create pod sandbox."
  exit 1
fi
echo "Pod created with ID: $POD_ID"

# Step 2: Create the container inside the pod
echo "Creating container inside the pod..."
CONTAINER_ID=$(crictl create $POD_ID $CONTAINER_CONFIG $SANDBOX_CONFIG)
if [ -z "$CONTAINER_ID" ]; then
  echo "Failed to create container."
  exit 1
fi
echo "Container created with ID: $CONTAINER_ID"

# Step 3: Start the container
echo "Starting container..."
crictl -t 100s start $CONTAINER_ID
if [ $? -ne 0 ]; then
  echo "Failed to start container."
  exit 1
fi
echo "Container started successfully."

# Step 4: Fetch and print container logs
echo "Fetching logs from container..."
crictl logs -f $CONTAINER_ID

exit 0