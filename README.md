# terraform-provider-godaddy
[Terraform](https://www.terraform.io/) plugin for managing domain records

[![ci](https://github.com/jeffbeard/terraform-provider-godaddy/actions/workflows/ci.yml/badge.svg)](https://github.com/jeffbeard/terraform-provider-godaddy/actions/workflows/ci.yml)
[![release](https://github.com/jeffbeard/terraform-provider-godaddy/actions/workflows/release.yml/badge.svg)](https://github.com/jeffbeard/terraform-provider-godaddy/actions/workflows/release.yml)

This is a fork of [n3integration/terraform-provider-godaddy](https://github.com/n3integration/terraform-provider-godaddy),
which has had no releases since v1.9.1 (December 2022). Starting with v1.10.0 this fork adds GoDaddy
Personal Access Token authentication. Releases up to v1.9.1 are published by the upstream project.

<dl>
  <dt>Terraform v1.3.0 and later, Personal Access Token support</dt>
  <dd>https://github.com/jeffbeard/terraform-provider-godaddy/releases/tag/v1.10.0</dd>
  <dt>Terraform v1.3.0</dt>
  <dd>https://github.com/n3integration/terraform-provider-godaddy/releases/tag/v1.9.0</dd>
  <dt>Terraform v0.14.x</dt>
  <dd>https://github.com/n3integration/terraform-provider-godaddy/releases/tag/v1.8.7</dd>
  <dt>Terraform v0.12.x</dt>
  <dd>https://github.com/n3integration/terraform-provider-godaddy/releases/tag/v1.7.3</dd>
  <dt>Terraform v0.11.x</dt>
  <dd>https://github.com/n3integration/terraform-provider-godaddy/releases/tag/v1.6.4</dd>
  <dt>Terraform v0.10.x</dt>
  <dd>https://github.com/n3integration/terraform-provider-godaddy/releases/tag/v1.5.0</dd>
  <dt>Terraform v0.9.x</dt>
  <dd>https://github.com/n3integration/terraform-provider-godaddy/releases/tag/v1.3.0</dd>
  <dt>Terraform v0.8.x</dt>
  <dd>https://github.com/n3integration/terraform-provider-godaddy/releases/tag/v1.2.3</dd>
  <dt>Terraform v0.7.x</dt>
  <dd>https://github.com/n3integration/terraform-provider-godaddy/releases/tag/v1.0.0</dd>
</dl>

## Installation

The provider is published to the Terraform Registry as
[`jeffbeard/godaddy`](https://registry.terraform.io/providers/jeffbeard/godaddy). Declare it and run
`terraform init`:

```terraform
terraform {
  required_providers {
    godaddy = {
      source  = "jeffbeard/godaddy"
      version = "~> 1.10"
    }
  }
}
```

### Migrating from n3integration/godaddy

The resource and its schema are unchanged, so existing state carries over. Change `source` to
`jeffbeard/godaddy` as above, then point the state at the new address and re-initialize:

```bash
terraform state replace-provider n3integration/godaddy jeffbeard/godaddy
terraform init -upgrade
```

### Manual installation

To install without the registry, for example in an air-gapped environment:

1. Download the archive for your platform, plus the `SHA256SUMS` and `SHA256SUMS.sig` files, from
   https://github.com/jeffbeard/terraform-provider-godaddy/releases
2. Verify the download (see [Verifying a release](RELEASING.md#verifying-a-release)).
3. Unpack the binary into the plugin directory for your platform, keeping its file name. For v1.10.0
   on Apple Silicon:

   ```bash
   dir=~/.terraform.d/plugins/registry.terraform.io/jeffbeard/godaddy/1.10.0/darwin_arm64
   mkdir -p "$dir"
   unzip terraform-provider-godaddy_1.10.0_darwin_arm64.zip -d "$dir"
   ```

   Replace `darwin_arm64` with your platform, for example `linux_amd64` or `windows_amd64`. On Windows
   the plugin directory is `%APPDATA%\terraform.d\plugins`.
4. Run `terraform init`. Terraform uses the local copy instead of contacting the registry for this
   provider.

To build from source instead, run `make local`, which installs into the same plugin directory.

## Authentication

The provider authenticates with a GoDaddy [Personal Access Token](https://developer.godaddy.com/en/docs/api-users/auth) (PAT).
Generate one from the GoDaddy developer portal with the following scopes:

* `domains.dns:update`
* `domains.domain:read` (GoDaddy requires this even for write operations)

The token can be supplied via the `token` provider attribute or the `GODADDY_API_TOKEN` environment variable.

```bash
export GODADDY_API_TOKEN=xxxxxxxx
```

```terraform
provider "godaddy" {
  token = "xxxxxxxx"
}
```

### Legacy API key and secret (deprecated)

GoDaddy is sunsetting the two-part API key/secret credential. The provider still accepts it via the
`key` and `secret` attributes or the `GODADDY_API_KEY` and `GODADDY_API_SECRET` environment variables,
and emits a deprecation warning whenever they are in use, including when supplied via environment
variables. If both a token and a key/secret
pair are present, the token is used. Support for `key` and `secret` will be removed in a future major
version; migrate to a token.

```terraform
provider "godaddy" {
  key    = "abc"
  secret = "123"
}
```

## Domain Record Resource
A `godaddy_domain_record` resource requires a `domain`. If the domain is not registered under the account that owns the credential, an optional `customer` number can be specified.
Additionally, one or more `record` instances are required. For each `record`, the `name`, `type`, and `data` attributes are required. `MX` records can optionally specify `priority` or will default to `0`. Address and NameServer records can be
defined using shorthand-notation as `addresses = [""]` or `nameservers = [""]`, respectively unless you need to override the default time-to-live (3600). The available record
types include:

* A
* AAAA
* CAA
* CNAME
* MX
* NS
* SOA
* SRV
* TXT

```terraform
resource "godaddy_domain_record" "gd-fancy-domain" {
  domain   = "fancy-domain.com"

  // required if provider key does not belong to customer
  customer = "1234"

  // specify zero or more record blocks
  // a record block allows you to configure A, or NS records with a custom time-to-live value
  // a record block also allow you to configure AAAA, CNAME, TXT, or MX records
  record {
    name = "www"
    type = "CNAME"
    data = "fancy.github.io"
    ttl = 3600
  }

  record {
    name = "@"
    type = "MX"
    data = "aspmx.l.google.com."
    ttl = 600
    priority = 1
  }

  record {
    name      = "@"
    type      = "SRV"
    data      = "host.example.com"
    ttl       = 3600
    service   = "_ldap"
    protocol  = "_tcp"
    port      = 389
  }

  // specify any A records associated with the domain
  addresses   = ["192.168.1.2", "192.168.1.3"]

  // specify any custom nameservers for your domain
  // note: godaddy now requires that the 'custom' nameservers are first supplied through the ui
  nameservers = ["ns7.domains.com", "ns6.domains.com"]
}
```

## Building for Linux

```bash
make linux
```

### Additional Information
If your zone contains existing data, please ensure that your Terraform resource configuration includes all existing records, otherwise they will be removed.

This plugin also supports Terraform's [import](https://www.terraform.io/docs/import/usage.html) feature. This will at least allow you to determine the changes introduced
through `terraform plan` and update the resource configuration accordingly to preserve existing data. The supplied resource `id` to the `terraform import` command should
be the name of the domain that you would like to import. Although this is currently a manual workaround, this plugin will be updated when Terraform includes support for
fully automated imports.

#### Import Example
```bash
terraform import godaddy_domain_record.gd-fancy-domain fancy-domain.com
```

## License

Copyright 2023 n3integration@gmail.com

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
