# secret-mirror-controller

Copies a Secret into every namespace matching a label selector, and keeps it that way.

[Launchpad](https://github.com/cujarrett/launchpad) hands each guest sandbox one of five fixed demo
slots, and each slot owns a long-lived TLS certificate that lives in `demo-certs`. Those
certificates have to appear inside the sandbox namespace, or cert-manager issues new ones against
hostnames that are already at Let's Encrypt's rate limit. This controller makes that happen, and
keeps happening - a copy deleted by hand comes back, a renewed certificate propagates, and a sandbox
that is torn down leaves nothing behind.

## The API

```yaml
apiVersion: platform.local.lab/v1alpha1
kind: SecretMirror
metadata:
  name: demo1-tls
  namespace: demo-certs
spec:
  sourceSecret: demo1-tls # a Secret in this namespace
  targetNamespaceSelector:
    matchLabels:
      launchpad.local.lab/slot: demo1
```

Selection is by label rather than by name because guests choose their own workspace
names. The label names the *slot*, not "is a sandbox" - the certificates are per-slot, so
a blanket match would put slot 1's certificate in slot 3's namespace.

```console
$ kubectl get secretmirrors -A
NAMESPACE     NAME        SOURCE      COPIES   READY   AGE
demo-certs    demo1-tls   demo1-tls   1        True    9d
```

## Two rules

**It never modifies a Secret it did not create.** Every copy is labelled
`platform.local.lab/mirrored-by`. A Secret at the target name without that label is left
untouched and reported as a conflict on the mirror's status, never overwritten.

**Deleting a mirror deletes its copies.** ownerReferences cannot express this - the
garbage collector ignores an owner in another namespace - so a finalizer holds the mirror
in the API until the copies are gone.

## Permissions

The controller holds **no cluster-wide access to Secrets**. It reads its own CRs,
lists Namespaces, and writes Events cluster-wide, and nothing else.

| Grant | Scope |
|---|---|
| `secret-mirror-controller` | cluster-wide; SecretMirrors, Namespaces (read), Events |
| `secret-mirror-reader` | bound in `demo-certs` only; read the source certificates |
| `secret-mirror-writer` | bound per sandbox namespace by launchpad-api at creation time |

That last binding is the interesting one. Sandbox namespaces are named by guests, so they
cannot be listed anywhere in advance - launchpad-api renders the RoleBinding beside the
namespace itself, so the grant appears with the sandbox and disappears with it.

The same constraint shapes the watches. Watching a type means listing it cluster-wide, so
only the source namespace's Secrets are watched (`--source-namespace`, default
`demo-certs`); target Secrets are read directly from the API server instead of a cache. A
new sandbox still triggers work immediately through the Namespace watch.

## Running it

Deployed by ArgoCD from the homelab repo at `cluster/secret-mirror-controller/`. Pushing
to `main` builds an ARM64 image and bumps the tag there.

Locally, against whatever cluster your kubeconfig points at:

```sh
make install                                  # apply the CRD
go run ./cmd --source-namespace=demo-certs
```

## Development

`make` is the entrypoint - see [CLAUDE.md](./CLAUDE.md) for the full table.

```sh
make generate manifests                       # after editing api/
go test -race -skip TestControllers ./...     # fast, no envtest binaries needed
make test                                     # full suite, downloads envtest
```

After changing `api/`, run `make generate manifests` and commit the result. CI pushes the
regenerated CRD into the homelab repo on merge, so the cluster never runs a stale schema.
