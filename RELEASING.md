# Releasing

Releases are built in the `release` GitHub Actions workflow when a `v*` tag is pushed. It has two jobs:

- **build** has no access to secrets. It refuses tags that are not on `main`, runs the unit tests,
  builds every platform with GoReleaser, writes the checksum file, and records build provenance.
- **sign** waits for approval of the `release` environment. It signs the checksum file with GPG and
  creates a **draft** GitHub release. It checks out no code, so nothing from the repository or its
  dependencies runs next to the signing key.

A maintainer inspects the draft and publishes it.

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

The backup must be this export. The files under `~/.gnupg/private-keys-v1.d/` are GnuPG's internal
storage and cannot be imported. Prove the backup works before relying on it: import it into an empty
keyring and sign something.

```bash
export GNUPGHOME=$(mktemp -d)
gpg --import release-signing-key.asc        # from the backup
echo test | gpg --clearsign > /dev/null && echo "backup works"
gpgconf --kill all; rm -rf "$GNUPGHOME"; unset GNUPGHOME
```

Once the key is in the `release` environment (next step) and the backup is proven, delete the private
key from the workstation with `gpg --delete-secret-keys "$FPR"`. Day-to-day work does not need it.

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
  then update the `GPG_PRIVATE_KEY` secret and the published public key. For a new key, also update
  `EXPECTED_FINGERPRINT` in `release.yml`, the fingerprint in `SECURITY.md`, and the key registered at
  the Terraform Registry. The sign job refuses any key whose fingerprint does not match.
- If the key or passphrase may have leaked: delete the secrets, publish the revocation certificate,
  create a new key, and say in the release notes which releases the old key signed.
- Workflow actions are pinned to commit SHAs. Review the upstream diff before bumping a pin.
- Keep the sign job minimal. It must not check out the repository, set up a toolchain, or run a build
  tool. Anything that executes code belongs in the build job, which has no secrets.
- The release workflow is triggered only by tag pushes. Workflows triggered by pull requests from
  forks do not receive secrets; keep it that way and never add the signing step to `ci.yml`.

## Pinned tool versions

The Go and GoReleaser versions are exact, in both `ci.yml` and `release.yml`, so a release is never
built with a tool version that CI has not already run. Dependabot updates the pinned actions and the Go
modules, but not these two version strings. Bump them by hand in a pull request, both files together,
and prefer a version that has been out for a week or more.

## Cutting a release

1. Set the version in `install.sh` (the Makefile reads it from there) and add a row to the release
   list in `README.md`. Merge to `main`.
2. Confirm CI is green on `main`.
3. Tag the merge commit and push the tag:

   ```bash
   git checkout main && git pull
   git tag -a v1.10.0 -m "v1.10.0"
   git push origin v1.10.0
   ```

4. Wait for the `build` job to finish, then approve the `release` environment deployment in the
   Actions tab. Approve only a run you started: check the tag and the commit it points at.
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

The signature shows that the release workflow signed the checksum file. Build provenance shows where a
file was built: which repository, workflow, and commit. Releases after v1.10.0 carry it. Check any zip
with the GitHub CLI:

```bash
gh attestation verify terraform-provider-godaddy_1.10.1_linux_amd64.zip \
  --repo jeffbeard/terraform-provider-godaddy
```

## If a release or the signing key is compromised

1. Stop further releases: delete the `GPG_PRIVATE_KEY` and `PASSPHRASE` secrets from the `release`
   environment.
2. Find out what was published. Compare the release list and the Actions run history against the
   releases you approved.
3. Withdraw any bad version from the Terraform Registry so new installs cannot select it, then mark the
   GitHub release as compromised in its notes. Published releases are immutable and their tags cannot
   be moved, so a bad version is superseded, not replaced.
4. If the key may have leaked, publish its revocation certificate, remove it from the Terraform
   Registry signing keys, and create a new key as in the one-time setup.
5. Publish a fixed version under a new number, and a security advisory on the repository naming the
   affected versions and what users should rotate. A malicious build could have read users' GoDaddy
   API tokens.
