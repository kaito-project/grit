#!/bin/bash

echo "Fetching pod and container IDs..."
POD_ID=$(crictl pods -q)
CONTAINER_ID=$(crictl ps -a -q)

# Stop and remove the container if it exists
if [ -n "$CONTAINER_ID" ]; then
  echo "Stopping container: $CONTAINER_ID"
  crictl stop $CONTAINER_ID
  echo "Removing container: $CONTAINER_ID"
  crictl rm $CONTAINER_ID
else
  echo "No running containers found."
fi

# Remove the pod if it exists
if [ -n "$POD_ID" ]; then
  echo "Removing pod: $POD_ID"
  crictl stopp $POD_ID  # Stop the pod
  crictl rmp $POD_ID    # Remove the pod
else
  echo "No running pods found."
fi

echo "Cleanup completed."

exit 0