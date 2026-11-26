# Security

This provider runs with a GoDaddy API token and can change DNS records, so problems in it or in its
release process matter. Reports are welcome.

## Reporting a vulnerability

Report privately through GitHub: open the repository's **Security** tab and choose
**Report a vulnerability**. Please do not open a public issue or pull request for a security problem.

Include what you found, how to reproduce it, and which version is affected. You should get a first
reply within a week.

Also report it this way if you believe a published release, the signing key, or the repository itself
has been tampered with.

## Supported versions

Only the latest release receives fixes.

## Verifying what you install

Every release from v1.10.0 on is signed with this key:

```
999751B7B6512241E73B65532D2D237C15BE1EAA
```

The public key is in [`release-signing-key.pub.asc`](release-signing-key.pub.asc). Terraform checks the
signature automatically when it installs the provider from the Terraform Registry. To check a manual
download, and to confirm that a file was built by this repository's release workflow, see
[Verifying a release](RELEASING.md#verifying-a-release).

Commit `.terraform.lock.hcl` in your configurations. It pins the checksums of the provider version you
reviewed, so a changed binary under the same version number is rejected.
