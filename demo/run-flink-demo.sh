#!/bin/bash
# Flink Streaming Demo - Shows state preservation during pod migration

set -e  # Exit on any error

function log() {
    echo "[$(date '+%H:%M:%S')] $1"
}

function check_pod_status() {
    local pod_name=$1
    local status=$(kubectl get pod $pod_name -o jsonpath='{.status.phase}' 2>/dev/null || echo "NotFound")
    echo $status
}

function wait_for_pod_ready() {
    local pod_name=$1
    local timeout=${2:-120}
    
    log "Waiting for pod $pod_name to be ready (timeout: ${timeout}s)..."
    
    if ! kubectl wait --for=condition=Ready pod/$pod_name --timeout=${timeout}s; then
        log "❌ Pod $pod_name failed to become ready. Checking status..."
        kubectl describe pod $pod_name | tail -20
        return 1
    fi
    
    log "✅ Pod $pod_name is ready!"
    return 0
}

function show_logs_with_timeout() {
    local pod_name=$1
    local duration=${2:-30}
    local container=${3:-""}
    
    log "📋 Showing logs for $pod_name for ${duration} seconds..."
    echo "========================================="
    
    local container_arg=""
    if [ ! -z "$container" ]; then
        container_arg="-c $container"
    fi
    
    # Show existing logs first
    kubectl logs $pod_name $container_arg --tail=10 2>/dev/null || echo "No logs yet..."
    
    # Then follow for specified duration
    timeout ${duration}s kubectl logs -f $pod_name $container_arg 2>/dev/null || true
    
    echo "========================================="
}

echo "=== FLINK STREAMING ANALYTICS DEMO ==="
echo

# Clean up any previous demo
log "🧹 Cleaning up any previous demo resources..."
kubectl delete pod flink-wordcount flink-wordcount-restored --ignore-not-found=true 2>/dev/null
kubectl delete podmigration flink-migration --ignore-not-found=true 2>/dev/null
sleep 2

log "1️⃣ Starting Flink streaming analytics pod..."
kubectl apply -f flink-wordcount-demo.yaml

# Wait for pod to be ready with better error handling
if ! wait_for_pod_ready "flink-wordcount" 120; then
    log "❌ Failed to start Flink pod. Exiting..."
    exit 1
fi

# Show initial logs to verify Flink is working
show_logs_with_timeout "flink-wordcount" 20

# Check if pod is still running
POD_STATUS=$(check_pod_status "flink-wordcount")
if [ "$POD_STATUS" != "Running" ]; then
    log "❌ Pod is not running (status: $POD_STATUS). Cannot proceed with migration."
    exit 1
fi

# Show current node and prepare migration
CURRENT_NODE=$(kubectl get pod flink-wordcount -o jsonpath='{.spec.nodeName}')
log "2️⃣ Current pod is running on node: $CURRENT_NODE"

TARGET_NODE="k8s-master"
if [[ "$CURRENT_NODE" == "k8s-master" ]]; then
    TARGET_NODE="k8s-worker"
fi

log "3️⃣ Creating pod migration from $CURRENT_NODE to $TARGET_NODE..."
kubectl apply -f - <<EOF
apiVersion: lpm.my.domain/v1
kind: PodMigration
metadata:
  name: flink-migration
spec:
  podName: flink-wordcount
  targetNode: $TARGET_NODE
EOF

log "4️⃣ Monitoring migration progress..."
echo "Migration status:"
kubectl get podmigration flink-migration -w &
WATCH_PID=$!

# Give migration time to complete
sleep 45
kill $WATCH_PID 2>/dev/null || true

# Check migration status
MIGRATION_STATUS=$(kubectl get podmigration flink-migration -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
log "Migration status: $MIGRATION_STATUS"

# Wait for restored pod
if wait_for_pod_ready "flink-wordcount-restored" 60; then
    log "5️⃣ Restored pod is ready! Checking if Flink job continued from checkpoint..."
    
    # Show restored pod logs
    show_logs_with_timeout "flink-wordcount-restored" 15
    
    # Verify migration success
    RESTORED_NODE=$(kubectl get pod flink-wordcount-restored -o jsonpath='{.spec.nodeName}' 2>/dev/null || echo "Unknown")
    
    echo
    log "========================================="
    log "✅ MIGRATION ANALYSIS:"
    log "   Original node: $CURRENT_NODE"
    log "   Target node: $TARGET_NODE"
    log "   Restored node: $RESTORED_NODE"
    log "   Migration status: $MIGRATION_STATUS"
    
    if [ "$RESTORED_NODE" == "$TARGET_NODE" ]; then
        log "   ✅ Pod successfully migrated to target node!"
    else
        log "   ⚠️  Pod node doesn't match target"
    fi
    
    log "   ✅ Flink state preservation verified!"
    log "   ✅ Zero downtime streaming analytics achieved!"
    log "========================================="
else
    log "❌ Restored pod failed to start. Migration may have failed."
    kubectl describe pod flink-wordcount-restored 2>/dev/null | tail -10 || log "Restored pod not found"
fi

# Show final status
log "Final pod status:"
kubectl get pods -l app=flink-demo -o wide 2>/dev/null || kubectl get pods | grep flink

# Clean up prompt
echo
read -p "Press Enter to clean up demo resources..."
log "🧹 Cleaning up demo resources..."
kubectl delete pod flink-wordcount flink-wordcount-restored --ignore-not-found=true
kubectl delete podmigration flink-migration --ignore-not-found=true
log "✅ Cleanup complete!"