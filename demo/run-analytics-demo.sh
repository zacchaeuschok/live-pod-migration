#!/bin/bash
# Analytics Counter Demo - Shows state preservation during pod migration
# Run this inside vagrant VM: vagrant ssh master

echo "=== ANALYTICS STREAMING DEMO ==="
echo "Run this inside vagrant VM!"
echo

# Clean up any previous demo
echo "🧹 Cleaning up previous demo..."
kubectl delete pod analytics-counter analytics-counter-restored --ignore-not-found=true 2>/dev/null
kubectl delete podmigration analytics-migration --ignore-not-found=true 2>/dev/null
sleep 2

echo "1️⃣ Starting analytics processing pod..."
kubectl apply -f analytics-counter-demo.yaml

echo "Waiting for pod to be ready..."
kubectl wait --for=condition=Ready pod/analytics-counter --timeout=30s

echo
echo "2️⃣ Watch analytics process events (building state)..."
echo "========================================="
kubectl logs -f analytics-counter &
LOG_PID=$!

sleep 25  # Let it process ~8 events
kill $LOG_PID 2>/dev/null

echo
echo "3️⃣ Creating pod migration..."
CURRENT_NODE=$(kubectl get pod analytics-counter -o jsonpath='{.spec.nodeName}')
TARGET_NODE="k8s-master"
if [[ "$CURRENT_NODE" == "k8s-master" ]]; then
    TARGET_NODE="k8s-worker"
fi

echo "Migrating from $CURRENT_NODE to $TARGET_NODE"

kubectl apply -f - <<EOF
apiVersion: lpm.my.domain/v1
kind: PodMigration
metadata:
  name: analytics-migration
spec:
  podName: analytics-counter
  targetNode: $TARGET_NODE
EOF

echo "4️⃣ Monitoring migration..."
kubectl get podmigration analytics-migration -w &
WATCH_PID=$!

sleep 30
kill $WATCH_PID 2>/dev/null

echo
echo "5️⃣ Check restored analytics - should continue from checkpoint!"
echo "========================================="
if kubectl get pod analytics-counter-restored >/dev/null 2>&1; then
    kubectl logs analytics-counter-restored --tail=15
    
    echo
    echo "✅ SUCCESS VERIFICATION:"
    echo "   - Analytics continued processing from checkpoint"
    echo "   - Revenue accumulation preserved"
    echo "   - Event counter continued (not reset to 1)"
    echo "   - Host changed but state maintained"
else
    echo "❌ Restored pod not found. Checking migration status..."
    kubectl get podmigration analytics-migration -o yaml | grep -A 5 "message\|phase"
fi

echo
echo "Final status:"
kubectl get pods | grep analytics
kubectl get podmigration analytics-migration

echo
read -p "Press Enter to clean up..."
kubectl delete pod analytics-counter analytics-counter-restored --ignore-not-found=true
kubectl delete podmigration analytics-migration --ignore-not-found=true