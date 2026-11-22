# Changelog

## v0.18.0

- `redfish session login --session-store REF` keeps the BMC session token in a secret manager
  (`keychain:`, `secretservice:SERVICE/ACCOUNT`, `keepassxc:`) instead of the 0600 session file, which then holds
  the endpoint, the session URI and the reference and no token. `session logout` and `session clear --session`
  delete the entry after ending the session. Without the option nothing changes, and old session files still load.
- Requires `github.com/go-authn/secretsource` v0.11.0 (`Delete`). The library package is unchanged and still uses
  only the standard library.
