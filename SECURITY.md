# Security policy

LUM drives updates, snapshots and scripts on your infrastructure, so security reports are taken seriously.

## Supported versions

Only the latest release receives security fixes.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through GitHub:
**Security → Report a vulnerability** on this repository
(<https://github.com/Wolfx56/lum/security/advisories/new>).

Include the affected version, a description of the issue, and steps to reproduce.
You should get a first answer within a week. Once a fix is released, the advisory is published and you are credited unless you prefer otherwise.

## Scope

In scope: the server (`lum-server`), the agent (`lum-agent`), the agent install script and the web UI.
Typical concerns: authentication bypass, running commands on agents outside the declared scripts, secret leaks in logs/API/UI, CSRF.

Out of scope: vulnerabilities of the applications LUM updates, and deployments that disable authentication (`auth.mode: none`) on an untrusted network.
