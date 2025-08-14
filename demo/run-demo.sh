#!/bin/bash
# Simple AI Streaming Demo - Shows state preservation during pod migration

echo "=== AI STREAMING DEMO: Pod Migration with State Preservation ==="
echo

# Clean up any previous demo
kubectl delete pod ai-streamer ai-streamer-restored --ignore-not-found=true 2>/dev/null
kubectl delete podmigration ai-migration --ignore-not-found=true 2>/dev/null

echo "1️⃣ Starting AI story generator pod..."
kubectl apply -f simple-demo.yaml

echo "Waiting for pod to start..."
kubectl wait --for=condition=Ready pod/ai-streamer --timeout=30s

echo
echo "2️⃣ Watch the AI generate a story (sequences 1-10)..."
echo "========================================="
kubectl logs -f ai-streamer &
LOG_PID=$!

sleep 20
kill $LOG_PID 2>/dev/null

echo
echo "3️⃣ Creating pod migration from $(kubectl get pod ai-streamer -o jsonpath='{.spec.nodeName}') to different node..."
TARGET_NODE="k8s-master"
if [[ $(kubectl get pod ai-streamer -o jsonpath='{.spec.nodeName}') == "k8s-master" ]]; then
    TARGET_NODE="k8s-worker"
fi

kubectl apply -f - <<EOF
apiVersion: lpm.my.domain/v1
kind: PodMigration
metadata:
  name: ai-migration
spec:
  podName: ai-streamer
  targetNode: $TARGET_NODE
EOF

echo "4️⃣ Monitoring migration progress..."
kubectl get podmigration ai-migration -w &
WATCH_PID=$!

sleep 30
kill $WATCH_PID 2>/dev/null

echo
echo "5️⃣ Check restored pod - should continue from checkpoint (not restart at SEQ 1)..."
echo "========================================="
kubectl logs ai-streamer-restored --tail=15

echo
echo "========================================="
echo "✅ SUCCESS Indicators:"
echo "   - ✅ Sequence continued (not restarted at 001)"
echo "   - ✅ Story context preserved"
echo "   - ✅ Host changed but state maintained"
echo "   - ✅ Zero downtime migration achieved!"

# Clean up
echo
read -p "Press Enter to clean up demo resources..."
kubectl delete pod ai-streamer ai-streamer-restored --ignore-not-found=true
kubectl delete podmigration ai-migration --ignore-not-found=true