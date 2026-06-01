# troublemaker

A simple service that simulate misbehaving service during deployment.

## Deploying on Kubernetes

A Helm chart is available in [`kubernetes/`](kubernetes/). See
[`kubernetes/README.md`](kubernetes/README.md) for configuration and usage.

```bash
helm install troublemaker kubernetes/ -n troublemaker --create-namespace
```
