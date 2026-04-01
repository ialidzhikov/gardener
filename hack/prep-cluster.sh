# Delete CR that fill interfere with recovery
kubectl delete csr $(kubectl get csr | grep 'kubernetes.io/kube-apiserver-client ' | cut -f 1 -d ' ' | tr  '\n' ' ')
kubectl apply -f <(kubectl get mr shoot-core-coredns -o yaml | sed 's@  - resources.gardener.cloud/gardener-resource-manager@@')
kubectl delete mr shoot-core-coredns

echo "Preparing cluster for recovery by creating workload and exporting secrets..."
kubectl get secret -o yaml > secrets.yaml