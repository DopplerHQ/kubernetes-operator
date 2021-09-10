# Processors

By default, the operator syncs secret values as they are in Doppler to an [`Opaque` Kubernetes secret](https://kubernetes.io/docs/concepts/configuration/secret/) as Key / Value pairs. In some cases, the value stored in Doppler is not the format required for your Kubernetes deployment. For example, Base64 encoded `.p12` key file that needs to be decoded for mounting in a container in its original binary format.

Processors provide a mechanism to achieve this.

Below is the Doppler Secret used in the Getting Started example. We've added a new field to the spec called `processors`.

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
  # An object mapping secret names to processor objects
  processors:
    MY_SECRET:
      type: plain
```

We currently have one processor for the `MY_SECRET` secret. During parsing, if there is a secret in your Doppler config called `MY_SECRET`, it will be processed using the `plain` parser before it's saved into your managed Kubernetes secret. If you do not specify processors (or you don't specify a processor for a secret in your config), the `plain` processor will be used by default.

You can have any number of processor:

```yaml
processors:
  MY_SECRET:
    type: plain
  OTHER_SECRET:
    type: plain
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
