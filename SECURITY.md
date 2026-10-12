# Security Policy

## Supported versions

Security fixes are applied to the `main` branch, which is what the public demo
runs.

## Reporting a vulnerability

Do not open a public Issue for a suspected vulnerability. Use GitHub's
[private vulnerability reporting](https://github.com/context4ai/sourcegraph/security/advisories/new)
and include:

- the affected commit or deployment and component;
- reproduction steps or a proof of concept;
- the security impact;
- any suggested mitigation;
- whether the issue has been disclosed elsewhere.

Relevant reports include authentication or authorization bypasses, access to
private repositories without permission, credential disclosure, path traversal,
unsafe Git or plugin execution, cross-site scripting or request forgery, and
denial of service through crafted queries or repositories.

General support questions and non-security bugs should use GitHub Discussions
or Issues.
