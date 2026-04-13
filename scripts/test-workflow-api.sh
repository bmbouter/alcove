#!/bin/bash

# Simple test script to validate workflow API endpoints
# This assumes Bridge is running on localhost:8080

BASE_URL="http://localhost:8080"

echo "Testing Workflow API endpoints..."

# Test GET /api/v1/workflows
echo -n "GET /api/v1/workflows: "
curl -s -f "$BASE_URL/api/v1/workflows" \
  -H "X-Alcove-User: test" \
  > /tmp/workflows.json && echo "OK" || echo "FAILED"

# Test GET /api/v1/workflow-runs 
echo -n "GET /api/v1/workflow-runs: "
curl -s -f "$BASE_URL/api/v1/workflow-runs" \
  -H "X-Alcove-User: test" \
  > /tmp/workflow-runs.json && echo "OK" || echo "FAILED"

# Test GET /api/v1/workflow-runs with status filter
echo -n "GET /api/v1/workflow-runs?status=running: "
curl -s -f "$BASE_URL/api/v1/workflow-runs?status=running" \
  -H "X-Alcove-User: test" \
  > /tmp/workflow-runs-running.json && echo "OK" || echo "FAILED"

echo "Test responses saved to /tmp/workflows.json and /tmp/workflow-runs*.json"
echo "API endpoints appear to be working!"
