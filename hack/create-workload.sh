#!/usr/bin/env bash

kubectl create configmap experimental-configmap --from-literal=content='experimenting with control plane disaster recovery'

kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: date-echo
spec:
  containers:
  - name: date-echo
    image: busybox:1.36
    command: ["/bin/sh","-c"]
    args: ["while true; do date; sleep 3; done"]
  restartPolicy: Always
EOF

kubectl wait --for=condition=Ready pod/date-echo --timeout=60s
