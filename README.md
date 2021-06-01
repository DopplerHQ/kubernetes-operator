# Doppler Kubernetes Operator (Prerelease)

Automatically sync secrets from Doppler to Kubernetes and auto-reload deployments when secrets change.

## Step 1: Create a Kubernetes Configuration Doppler Project

Create a project in Doppler with a name like "kubernetes-setup". We'll use this project to store a few configuration items that we'll use in the next steps.

```bash
doppler projects create kubernetes-setup
```

Create the following secrets in the appropriate environment(s):

- `DOPPLER_TOKEN`: A Doppler Service Token to access the secrets that you want to sync with Kubernetes. For example, if are deploying a project called "backend" in your cluster, you'll want to generate a new [Doppler Service Token](https://docs.doppler.com/docs/enclave-service-tokens) for the "backend" project and store it in the "kubernetes-setup" project.
- `SECRET_NAME`: The name of the Kubernetes secret that will be created and synced by operator. This can be anything, as long as it doesn't conflict with existing secrets in Kubernetes.

For example:

```bash
# Generate a service token for "backend.dev" and set it as the value for "kubernetes-setup.dev.DOPPLER_TOKEN"
doppler -p kubernetes-setup -c dev secrets set DOPPLER_TOKEN=$(doppler -p backend -c dev configs tokens create kubernetes-setup --plain)
# Set "kubernetes-setup.dev.SECRET_NAME" to "backend-doppler-secret"
doppler -p kubernetes-setup -c dev secrets set SECRET_NAME="backend-doppler-secret"
# Setup Doppler to use the "kubernetes-setup" project for this directory
doppler setup -p kubernetes-setup -c dev
```

## Step 2: Deploy the Operator

Deploy the operator by running:

```bash
make deploy
```

This will use your locally-configured `kubectl` to:

- Create the resource definition for a `DopplerSecret`
- Setup a service account and RBAC role for the operator
- Create a deployment for the operator inside of the cluster

You can verify that the operator is running successfully in your cluster with the `rollout` command:

```bash
kubectl rollout status -w -n doppler-kubernetes-operator-system deployment/doppler-kubernetes-operator-controller-manager
```

## Step 3: Create a `DopplerSecret`

A `DopplerSecret` is a custom Kubernetes resource with a secret name and a Doppler Service Token.

When a `DopplerSecret` is created, the operator reconciles it by creating an associated Kubernetes secret and populates it with secrets fetched from the Doppler API in Key-Value format.

Use the Doppler CLI to inject secrets from our "kubernetes-setup" project into the template and apply the changes to Kubernetes directly. This allows you to configure Kubernetes with your Doppler Service Token without storing it on your local filesystem.

```bash
kubectl apply -f <(doppler secrets substitute config/samples/secrets_v1alpha1_dopplersecret.yaml)
```

Check that the associated Kubernetes secret has been created:

```sh
# List all Kubernetes secrets created by the Doppler controller
kubectl describe secrets --selector=dopplerSecret=true
```

The controller continuously watches for secret updates from Doppler and when detected, automatically and instantly updates the associated secret.

Next, we'll cover how to configure a deployment to use the Kubernetes secret and enable auto-reloading for Deployments.

## Step 3: Configuring a Deployment

### Using the Secret in a Deployment

To use the secret created by the operator, we can use the synced Kubernetes secret in one of three ways. These methods are also covered in greater detail in the [Kubernetes Secrets documentation](https://kubernetes.io/docs/concepts/configuration/secret/).

#### `envFrom`

The `envFrom` field will populate a container's environment variables using the secret's Key-Value pairs:

```yaml
envFrom:
  - secretRef:
    name: my-doppler-secret
```

#### `valueFrom`

The `valueFrom` field will inject a specific environment variable from the Kubernetes secret:

```yaml
env:
  - name: MY_APP_SECRET
    valueFrom:
      secretKeyRef:
        name: my-dopper-secret
        key: MY_APP_SECRET
```

#### `volume`

The `volume` field will create a volume that is populated with files containing the Kubernetes secret:

```yaml
volumes:
  - name: secret-volume
    secret:
      secretName: my-doppler-secret
```

Your deployment can use this volume by mounting it to the container's filesystem:

```yaml
volumeMounts:
  - name: secret-volume
    mountPath: /etc/secrets
    readOnly: true
```

### Automatic Redeployments

Adding automatic and instant reloading of a deployment requires just a single annotation on the Deployment:

```yaml
annotations:
  secrets.doppler.com/reload: 'true'
```

### Full Examples

Complete examples of these different deployment configurations can be found below:

- [`deployment-envfrom.yaml`](config/samples/deployment-envfrom.yaml)
- [`deployment-valuefrom.yaml`](config/samples/deployment-valuefrom.yaml)
- [`deployment-volume.yaml`](config/samples/deployment-volume.yaml)


To make testing easy, these deployment files have been written as templates which you can use with your `kubernetes-setup` project.

```sh
kubectl apply -f <(doppler secrets substitute config/samples/deployment-fromenv.yaml)
kubectl rollout status -w deployment/doppler-test-deployment-envfrom
```

Once the Deployment has completed, you can view the logs of the test container:

```sh
kubectl logs -lapp=doppler-test
```

## Debugging and Troubleshooting

- [`hack/get-secret.sh`](hack/get-secret.sh) can be used to fetch and decode a Kubernetes secret

## Development

This project uses the [Operator SDK](https://sdk.operatorframework.io).

When developing locally, you can run the operator using:

```bash
make install run
```

See the [Operator SDK Go Tutorial](https://sdk.operatorframework.io/docs/building-operators/golang/tutorial/#run-the-operator) for more information.
