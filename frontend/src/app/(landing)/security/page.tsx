import LegalPageLayout, {
  Section,
} from "@/components/landing/legal-page-layout";

export default function SecurityPage() {
  return (
    <LegalPageLayout title="Security" lastUpdated="March 18, 2026">
      <Section heading="Reporting a vulnerability">
        <p>
          If you discover a security vulnerability in 143, please report it
          responsibly. Do not open a public GitHub issue for security
          vulnerabilities.
        </p>
        <p>
          <strong className="font-medium opacity-80">
            Preferred method:
          </strong>{" "}
          Use GitHub&apos;s{" "}
          <a
            href="https://github.com/assembledhq/143/security/advisories/new"
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2"
          >
            private security advisory reporting
          </a>{" "}
          on the repository&apos;s Security tab.
        </p>
        <p>
          <strong className="font-medium opacity-80">
            Alternative:
          </strong>{" "}
          Email{" "}
          <a
            href="mailto:security@assembled.com"
            className="underline underline-offset-2"
          >
            security@assembled.com
          </a>
          , and review the repository&apos;s{" "}
          <a
            href="https://github.com/assembledhq/143/blob/main/SECURITY.md"
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2"
          >
            SECURITY.md
          </a>
          .
        </p>
      </Section>

      <Section heading="Supported versions">
        <p>
          We currently support the hosted service at 143.dev and the latest code
          on the repository&apos;s default branch. Because the open-source project
          moves quickly and does not currently publish long-term support
          branches, older forks or unpatched deployments may not receive
          coordinated fixes.
        </p>
      </Section>

      <Section heading="Safe harbor and testing rules">
        <p>
          We support good-faith security research that avoids privacy
          violations, service disruption, destructive testing, social
          engineering, spam, and persistence in other users&apos; data. Stop
          testing and contact us immediately if you access non-public data.
        </p>
      </Section>

      <Section heading="What to include">
        <ul className="list-disc pl-5 space-y-1.5">
          <li>Description of the vulnerability and its potential impact</li>
          <li>Steps to reproduce or a proof of concept</li>
          <li>Affected versions or components</li>
          <li>Any suggested fixes, if you have them</li>
        </ul>
      </Section>

      <Section heading="Response timeline">
        <ul className="list-disc pl-5 space-y-1.5">
          <li>
            <strong className="font-medium opacity-80">Acknowledgment:</strong>{" "}
            within 30 days
          </li>
          <li>
            <strong className="font-medium opacity-80">Investigation:</strong>{" "}
            within 60 days
          </li>
          <li>
            <strong className="font-medium opacity-80">Remediation target:</strong>{" "}
            we aim to remediate confirmed issues within 180 days when feasible,
            with coordinated disclosure timing based on severity,
            exploitability, and user impact
          </li>
        </ul>
        <p>
          We will keep you informed throughout the process. If you have not
          heard from us within the acknowledgment window, please follow up.
        </p>
      </Section>

      <Section heading="Security design">
        <p>143 is designed with security in mind:</p>
        <ul className="list-disc pl-5 space-y-1.5">
          <li>
            <strong className="font-medium opacity-80">Sandboxed execution</strong> -
            coding agents run in isolated containers, separate from the host
            system
          </li>
          <li>
            <strong className="font-medium opacity-80">Minimal permissions</strong> -
            integrations request only the scopes necessary to function
          </li>
          <li>
            <strong className="font-medium opacity-80">Hosted-service transport security</strong> -
            production traffic to 143.dev is intended to use TLS; self-hosted
            operators are responsible for their own deployment configuration
          </li>
          <li>
            <strong className="font-medium opacity-80">Credential handling</strong> -
            credentials are stored with platform protections, and some
            credentials may be injected into sandboxes when needed to run a
            configured agent or integration workflow
          </li>
          <li>
            <strong className="font-medium opacity-80">Sensitive state treatment</strong> -
            workspace snapshots and agent state are treated as sensitive because
            they may contain customer content and temporary credentials
          </li>
          <li>
            <strong className="font-medium opacity-80">Open source</strong> -
            the entire codebase is public and auditable under the MIT License
          </li>
        </ul>
      </Section>

      <Section heading="Scope">
        <p>
          This page covers the 143 open-source project and the hosted service at
          143.dev. Vulnerabilities in third-party dependencies should also be
          reported to the relevant maintainers, but we still want to know about
          them so we can patch or upgrade our usage promptly.
        </p>
      </Section>

      <Section heading="Recognition">
        <p>
          We appreciate the work of security researchers. With your permission,
          we are happy to credit you in the security advisory and release notes
          when a fix is available.
        </p>
      </Section>

      <Section heading="Contact">
        <p>
          For security questions that are not vulnerability reports, reach us
          at{" "}
          <a
            href="mailto:security@assembled.com"
            className="underline underline-offset-2"
          >
            security@assembled.com
          </a>
          .
        </p>
      </Section>
    </LegalPageLayout>
  );
}
