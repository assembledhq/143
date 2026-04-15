# Authorized Deploy Keys

Drop SSH public key files (`.pub`) into this directory to grant deploy access.

## Adding a new contributor

1. Have the contributor generate a key (if they don't have one):
   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/143-deploy -C "their-email@example.com"
   ```
2. Add their public key to this directory:
   ```bash
   cp ~/.ssh/143-deploy.pub deploy/authorized_keys/username.pub
   ```
3. Commit and push, then sync to all servers:
   ```bash
   make sync-keys
   ```

## Removing a contributor

Delete their `.pub` file, commit, and run:
```bash
make sync-keys          # dry run — verify the diff
make sync-keys APPLY=true  # push changes to all servers
```

## Security

Public keys are safe to store in a public repo -- they cannot be used to
authenticate without the corresponding private key. Review PRs that modify
this directory carefully, since merging a malicious key grants server access
after the next `sync-keys`.
