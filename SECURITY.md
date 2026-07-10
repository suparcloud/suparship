# Security Policy

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, email us at **security@suparcloud.io** with:

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Any suggested fixes (optional)

## What to Expect

- **Acknowledgment** within 48 hours
- **Status update** within 7 days
- **Coordinated disclosure** once a fix is available

We appreciate responsible disclosure and will credit reporters (unless you prefer anonymity).

## Supported Versions

| Version | Supported |
|---------|-----------|
| main    | ✅        |
| < v1.0  | Best effort |

## Security Best Practices

When using suparship:

- Never commit secrets to Git — use references only (`ref:secret-name.key`)
- Pin component versions explicitly
- Review generated manifests before applying
- Use `--profile=demo` only for local testing
