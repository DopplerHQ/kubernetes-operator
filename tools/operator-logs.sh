#!/usr/bin/env bash
OPERATOR_NAMESPACE=doppler-kubernetes-operator-system
OPERATOR_MANAGER=doppler-kubernetes-operator-controller-manager

kubectl rollout status -w -n $OPERATOR_NAMESPACE deployment/$OPERATOR_MANAGER
kubectl logs -f -n $OPERATOR_NAMESPACE deployments/$OPERATOR_MANAGER -c manager
