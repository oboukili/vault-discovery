# Vault-Discovery

Discovers and opens a local TCP tunnel to a Vault cluster's primary instance.
Useful as a "sidecar"/"companion app" when using the Terraform Vault provider.

All resources are available as Go library imports.

---

### Usage

**Basic**
```sh
# This is enough when running vault-discover from GCP
export GOOGLE_PROJECT=some-gcp-project
export TAG_INSTANCE_FILTER=vault
vault-discovery
```

**Advanced**
```sh
export GOOGLE_PROJECT=some-gcp-project
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/gcp/serviceaccount/token
export NAME_INSTANCE_FILTER=vault-
vault-discovery
```


---
### Features
* Vault cluster GCE discovery:
  * instances labels
  * instances tags
  * instances name (blob expression prefix)

---

### Configuration

**Environment variables**

|provider|variable name|required|default|description|
|---|---|---|---|---|
| |DISCOVERY_PROVIDER|*no*|*GCE*|For now, only the "GCE" provider is available.|
| |TLS_SKIP_VERIFY|*no*|*false*|Whether to skip or not Vault endpoint certificate.|
|gce|GOOGLE_PROJECT|**yes**| |Name of the GCP project to look for instances.|
|gce|GOOGLE_APPLICATION_CREDENTIALS|*no*| |Should not be needed when running from GCP.|
|gce|NAME_INSTANCE_FILTER|*no*| |Blob expression prefix to filter instances (example: 'vault-' == 'vault-*').|
|gce|LABEL_INSTANCE_FILTER|*no*| |**Single** instance label value to filter instances.|
|gce|TAG_INSTANCE_FILTER|*no*| |**Single** instance tag value to filter instances.|

---

### Roadmap
* Vault CA import
* Kubernetes discovery
* CLI configuration flags
* Unit tests
* Acceptance tests
* Exposing an interface{} API contract for new discovery providers

---

### Build
 
```sh
GOOS=linux go build -ldflags="-s -w" -o vault-discovery
```

---

### Contributing
#### New providers

Implementing new providers (kubernetes?) would only require to introduce a new Go package exposing a public getter function returning a (types.VaultTunnelCon, error) tuple (pending an interface{} API contract).
