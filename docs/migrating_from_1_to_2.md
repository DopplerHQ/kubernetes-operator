# Migrating from v1 to v2

Doppler Kubernetes Operator (DKO) v1 shipped with a very simple Helm chart; it did not support any custom values and it ignored the namespace provided by the user during `helm install`. The v1 chart created the `doppler-operator-system` namespace and installed all resources here.

Doppler Kubernetes Operator (DKO) v2 makes the Helm chart much more flexible but a few things must be considered when upgrading from v1 to v2.

**Will any resources be moved to the Helm installation namespace when I upgrade?**

If you installed the DKO Helm v1 chart in a namespace other than `doppler-operator-system`, the operator manager deployment ignored this configuration and was installed in the `doppler-operator-system` namespace. When you upgrade to the v2 chart, the deployment will be moved to the Helm installation namespace.

For example, if you followed the steps in our README to install DKO v1 (`helm install --generate-name doppler/doppler-kubernetes-operator`), the chart was installed in the `default` namespace and this is where the operator will move when you upgrade.

**I want the operator to continue running from `doppler-operator-system` but I installed the Helm chart in a different namespace.**

If you have installed the DKO Helm chart in a namespace other than `doppler-operator-system` but you want to keep the operator manager deployment running in `doppler-operator-system`, there is a migration path. First, upgrade to v1.6.1 to ensure that the `doppler-operator-system` namespace is marked with the `helm.sh/resource-policy: keep` annotation. This will allow you to uninstall the DKO Helm chart without destroying the `doppler-operator-system` namespace (and any `DopplerSecret` resources inside). You can then uninstall and reinstall the DKO Helm chart with version v2.0.0 (or any later version) in the `doppler-operator-system` namespace.

**`DopplerSecret` resources in the `doppler-operator-system` namespace have special permissions behavior, will this be preserved?**

In DKO v1, the operator manager treated `DopplerSecret` resources its own namespace (`doppler-operator-system`) with special permissions. `DopplerSecret` resources in this namespace were allowed to reference token and managed secrets anywhere in the cluster. `DopplerSecret` resources outside of this namespace could only reference token and managed secrets in the same namespace as the `DopplerSecret`.

In DKO v2, this special namespace definition has been moved to the `controllerManagerConfig.clusterDopplersecretNamespace` Helm value, with the default set to `doppler-operator-system`. This means that when you upgrade to v2, the `doppler-operator-system` namespace permissions behavior will be preserved, regardless of where the operator is running. You can choose to modify Helm value to change the cluster namespace or you can set it to the empty string (`""`) to disable the cluster permissions behavior entirely.
