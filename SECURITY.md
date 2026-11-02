# Security Policy

## Report a vulnerability

Report suspected vulnerabilities privately through
[GitHub's vulnerability reporting form](https://github.com/arkoren/arkoren/security/advisories/new).
You need a GitHub account to submit a report.

Do not disclose vulnerabilities in public issues, discussions, or pull requests
before coordinating with the maintainers. Never include live credentials,
private keys, or personal data in a report; use redacted examples or test values.

Include:

- The affected module, package, and version.
- Your Go version and operating system, plus backend versions if relevant.
- Steps to reproduce the problem, preferably with a minimal example.
- The security impact and any known workaround.

Use the private report to coordinate investigation, fixes, and public disclosure.
There is no guaranteed response or resolution time.

## Supported versions

Arkoren modules are versioned independently. Security fixes target the latest
published version of each affected module. Older versions have no guaranteed
security backports; you may need to upgrade to receive a fix.

Report suspected vulnerabilities even if you found them in an older version.
Include the exact affected versions so maintainers can assess the impact.
