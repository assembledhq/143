# Verified Domain Auto-Join

> **Status:** Implemented | **Last reviewed:** 2026-05-30

Organizations can verify ownership of an email domain and allow users with provider-verified email addresses on that domain to join automatically.

## Design

Admins manage verified domains under org settings. Creating a domain produces a DNS TXT challenge at:

```text
_143-domain-verification.<domain>
```

with a value shaped as:

```text
143-domain-verification=<token>
```

Verification succeeds only when the backend can resolve the exact TXT value. The row then moves from `pending` to `verified`.

Each domain controls whether auto-join is enabled and which membership role is granted. The default role is `member`, matching invitation defaults.

## Join Semantics

Auto-join is only evaluated for OAuth identities whose provider asserts that the email address is verified. In v1 this is wired for Google OAuth via the `email_verified` claim from the userinfo response. Email/password signup does not auto-join from a typed email address because the product does not yet verify ownership of that inbox during signup.

When a matching verified domain exists:

- existing users receive membership via `GrantAtLeast`, so auto-join never downgrades an existing higher role;
- `users.last_org_id` is set to the verified-domain org so fresh sessions land in that workspace;
- new Google OAuth users are created directly in the verified-domain org instead of receiving a personal bootstrap org.

## Security Notes

The ownership proof and user proof are intentionally separate:

- DNS TXT proves the organization controls the domain.
- Provider-verified email proves the joining user controls an address on that domain.

This avoids the unsafe path of granting access to anyone who can type `name@company.com` into password signup. A future email-verification flow can safely opt password signup into the same auto-join helper.
