machine=$1

# Delete CR that fill interfere with recovery
kubectl delete csr $(kubectl get csr | grep 'kubernetes.io/kube-apiserver-client ' | cut -f 1 -d ' ' | tr  '\n' ' ')
# Iterate all ManagedResources across all namespaces
kubectl get mr -A -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}' \
| while read -r ns name; do
  echo "Processing MR ${ns}/${name}..."
  # Remove the gardener-resource-manager finalizer and re-apply
  kubectl apply -n "$ns" -f <(
    kubectl get mr -n "$ns" "$name" -o yaml \
    | sed 's@  - resources.gardener.cloud/gardener-resource-manager.*@@'
  )
  # Delete the MR
  kubectl delete mr -n "$ns" "$name" --ignore-not-found
done
kubectl delete node "$machine"
kubectl get pods --all-namespaces \
  --field-selector=spec.nodeName="$machine" \
  -o jsonpath='{range .items[*]}{.metadata.namespace}{" "} {.metadata.name}{"\n"}{end}' \
| while read ns name; do
    kubectl delete pod "$name" -n "$ns" --grace-period=0 --force
  done
kubectl delete deployment -l app=gardener-resource-manager -A 
kubectl delete deployment machine-controller-manager --ignore-not-found

kubectl get secret -o yaml > secrets.yaml