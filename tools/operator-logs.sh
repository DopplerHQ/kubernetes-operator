#!/usr/bin/env bash

DEPLOYMENT_OPTIONS="$(kubectl get deployments -A -l 'control-plane=controller-manager' -o go-template --template='{{range .items}}{{.metadata.namespace}}/{{.metadata.name}}{{end}}')"
option_count=$(echo "$DEPLOYMENT_OPTIONS" | grep -c .)

if [ -z "$1" ]; then
  if [ "$option_count" -eq 0 ]; then
    echo "No deployments found with the specified label."
    exit 1
  elif [ "$option_count" -eq 1 ]; then
    OPERATOR_MANAGER="$DEPLOYMENT_OPTIONS"
  else
    if command -v fzf >/dev/null 2>&1; then
      OPERATOR_MANAGER=$(echo "$DEPLOYMENT_OPTIONS" | fzf --prompt="Select an operator deployment: ")
      if [ -z "$OPERATOR_MANAGER" ]; then
        echo "No selection made."
        exit 1
      fi
    else
      echo "Error: Multiple deployments found but 'fzf' is not installed to select one."
      echo "$DEPLOYMENT_OPTIONS"
      exit 1
    fi
  fi
else
  OPERATOR_MANAGER="$1"
fi

echo "Found $OPERATOR_MANAGER"
IFS="/" read -r OPERATOR_NAMESPACE OPERATOR_DEPLOYMENT <<<"$OPERATOR_MANAGER"

kubectl rollout status -w -n "$OPERATOR_NAMESPACE" "deployment/$OPERATOR_DEPLOYMENT"
kubectl logs -f -n "$OPERATOR_NAMESPACE" "deployments/$OPERATOR_DEPLOYMENT" -c manager
