---
page_title: "GoDaddy Provider"
subcategory: "infrastructure"
description: |-
  
---

# GoDaddy Provider

The GoDaddy Provider supports Terraform's `import` feature to make it easier to deal with existing records that are supplied by GoDaddy by default.

#### Import Example

To import any pre-existing GoDaddy resource data into your local Terraform state file, Terraform should be invoked using the `import` command with the fully-qualified resource name and domain name as command arguments. For example:

```bash
terraform import godaddy_domain_record.mydomain mydomain.com
```

## Schema

### Optional

- **token** (String, Sensitive) GoDaddy Personal Access Token. Requires the `domains.dns:update` and `domains.domain:read` scopes. Read from `GODADDY_API_TOKEN` when unset. Takes precedence over `key`/`secret`.
- **key** (String, Sensitive, Deprecated) GoDaddy API Key. Read from `GODADDY_API_KEY` when unset. Use `token` instead.
- **secret** (String, Sensitive, Deprecated) GoDaddy API Secret. Read from `GODADDY_API_SECRET` when unset. Use `token` instead.
- **baseurl** (String) GoDaddy Base URL(defaults to production).
