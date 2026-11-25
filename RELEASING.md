# Releasing

Releases are built by GoReleaser in the `release` GitHub Actions workflow when a `v*` tag is pushed.
The workflow runs the unit tests, builds every platform, signs the checksum file with GPG, and creates
a **draft** GitHub release. A maintainer inspects the draft and publishes it.

## One-time setup

### 1. Create a dedicated signing key

Use a key that exists only to sign releases of this project. Do not reuse a personal key: the private
key is stored in GitHub, and a leak should not affect anything else.

The Terraform Registry accepts RSA and DSA keys only, not the ECC keys recent GnuPG versions create by
default, so choose RSA 4096 explicitly:

```bash
gpg --full-generate-key
#   kind:        (1) RSA and RSA
#   size:        4096
#   expiry:      2y
#   real name:   terraform-provider-godaddy release signing
#   passphrase:  long, random, stored in a password manager
```

Record the fingerprint:

```bash
gpg --list-secret-keys --keyid-format long
export FPR=<40 character fingerprint>
```

### 2. Back up the key and its revocation certificate

GnuPG writes a revocation certificate to `~/.gnupg/openpgp-revocs.d/$FPR.rev` when the key is created.
Store that file, an export of the private key, and the passphrase somewhere offline or in a password
manager. Without the revocation certificate a leaked key cannot be invalidated.

```bash
gpg --armor --export-secret-keys "$FPR" > release-signing-key.asc   # move to the backup, then delete
```

### 3. Store the secrets on a protected environment

In the repository settings, create an environment named `release` and add yourself as a required
reviewer. Each release run then pauses for approval before it can read the key. Restrict the
environment's deployment tags to `v*`.

Add the two secrets to that environment. Piping the key keeps it out of shell history and off disk:

```bash
gpg --armor --export-secret-keys "$FPR" | gh secret set GPG_PRIVATE_KEY --env release
gh secret set PASSPHRASE --env release      # prompts for the value
```

### 4. Publish the public key

Users need the public key to verify downloads. Commit it to the repository and state the fingerprint
in the release notes:

```bash
gpg --armor --export "$FPR" > release-signing-key.pub.asc
```

To publish to the Terraform Registry later, add the same public key under
*User Settings > Signing Keys* at https://registry.terraform.io.

### 5. Protect release tags

Add a tag ruleset for `v*` that restricts who can create, update, or delete matching tags. Anyone who
can push a `v*` tag can otherwise request a signed release.

## Key hygiene

- Rotate before expiry: extend the expiry with `gpg --edit-key "$FPR" expire`, or create a new key,
  then update the `GPG_PRIVATE_KEY` secret and the published public key.
- If the key or passphrase may have leaked: delete the secrets, publish the revocation certificate,
  create a new key, and say in the release notes which releases the old key signed.
- Workflow actions are pinned to commit SHAs because they run with access to the key. Review the
  upstream diff before bumping a pin.
- The release workflow is triggered only by tag pushes. Workflows triggered by pull requests from
  forks do not receive secrets; keep it that way and never add the signing step to `ci.yml`.

## Cutting a release

1. Set the version in `install.sh` (the Makefile reads it from there) and add a row to the release
   list in `README.md`. Merge to `master`.
2. Confirm CI is green on `master`.
3. Tag the merge commit and push the tag:

   ```bash
   git checkout master && git pull
   git tag -a v1.10.0 -m "v1.10.0"
   git push origin v1.10.0
   ```

4. Approve the `release` environment deployment in the Actions tab.
5. Open the draft release, check that it contains a zip per platform, `SHA256SUMS`,
   `SHA256SUMS.sig`, and the manifest, verify it as below, write release notes, and publish.

## Verifying a release

```bash
gpg --import release-signing-key.pub.asc
gpg --verify terraform-provider-godaddy_1.10.0_SHA256SUMS.sig terraform-provider-godaddy_1.10.0_SHA256SUMS
shasum -a 256 --ignore-missing -c terraform-provider-godaddy_1.10.0_SHA256SUMS
```

`gpg --verify` must report a good signature from the fingerprint published for this project, and
`shasum` must report `OK` for each file you downloaded.
