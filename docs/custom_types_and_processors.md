# Custom Types and Processors

By default, the operator syncs secret values as they are in Doppler to an [`Opaque` Kubernetes secret](https://kubernetes.io/docs/concepts/configuration/secret/) as Key / Value pairs.

In some cases, the secret name or value stored in Doppler is not the format required for your Kubernetes deployment.
For example, you might have Base64-encoded TLS data that you want to copy to a native Kubernetes TLS secret.

Processors provide a mechanism to achieve this.

Below is the Doppler Secret used in the Getting Started example with some modifications.

```yaml
apiVersion: secrets.doppler.com/v1alpha1
kind: DopplerSecret
metadata:
  name: dopplersecret-test
  namespace: doppler-operator-system
spec:
  tokenSecret:
    name: doppler-token-secret
  managedSecret:
    name: doppler-test-secret
    namespace: default
    type: kubernetes.io/tls
    labels:
      doppler-secret-label: test
  # TLS secrets are required to have the secret fields `tls.crt` and `tls.key`
  processors:
    TLS_CRT:
      type: base64
      asName: tls.crt
    TLS_KEY:
      type: base64
      asName: tls.key
```

First, we've added a `type` field to the managed secret reference to define the `kubernetes.io/tls` managed secret type. When the operator creates the managed secret, it will have this Kubernetes secret type.

Managed secrets can also specify `labels` and `annotations`. These will be added verbatim to the `metadata` field in the resulting `Secret`.

We've also added a field called `processors`. Processors can make alterations to a secret's name or value before they are saved to the Kubernetes managed secret.

Kubernetes TLS manged secrets require the `tls.crt` and `tls.key` fields to be present in the secret data. To accommodate this, we're using two processors to remap our Doppler secrets named `TLS_CRT` and `TLS_KEY` to the correct field names with `asName`.

We can define the processor's `type` to instruct the operator to transform the secret value before saving it into the managed secret. Processors have a default type of `plain`, which treats the Doppler secret value as a plain string. In our example, we've provided the `base64` type which instructs the operator to process the Doppler secret value as Base64 encoded data.

**Note:** The processors are only applied if there is a Doppler secret in your config which corresponds with the processor name.

You can have any number of processors, each with different types and name mappings (or no name mapping at all).

```yaml
processors:
  MY_SECRET:
    type: plain
  OTHER_SECRET:
    type: plain
    asName: otherSecret
```

Below are the types of processors available to the operator:

## Plain

```yaml
type: plain
```

The default processor. This treats the data in the secret as plain string data.

## Base64

```yaml
type: base64
```

This processor will attempt to [Base64](https://en.wikipedia.org/wiki/Base64) decode the provided string and output the resulting bytes.

For example, the Base64 processor could be used to decode a Base64 encoded `.p12` file for mounting in a container in its original binary format.
