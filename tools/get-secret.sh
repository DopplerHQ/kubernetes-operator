#!/usr/bin/env bash

echo -e "\n### $1 ###\n"

kubectl get secret $1 -o go-template='{{range $k,$v := .data}}{{$k}}={{$v|base64decode}}{{"\n"}}{{end}}'
