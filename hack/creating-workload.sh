#!/usr/bin/env bash

kubectl create configmap experimental-configmap --from-literal=content='experimenting with control plane disaster recovery'

echo 'apiVersion: v1
kind: Pod
metadata:
  name: date-echo
spec:
  containers:
  - name: date-echo
    image: busybox:1.36
    command: ["/bin/sh","-c"]
    args: ["while true; do date; sleep 3; done"]
  restartPolicy: Always' > pod.yaml
  
kubectl apply -f pod.yaml

sleep 5

