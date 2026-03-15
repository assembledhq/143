import LegalPageLayout, {
  Section,
} from "@/components/landing/legal-page-layout";

export default function SecurityPage() {
  return (
    <LegalPageLayout title="Security" lastUpdated="March 15, 2026">
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
            Security Advisory reporting
          </a>{" "}
          — click &quot;Report a vulnerability&quot; on the repository&apos;s Security
          tab.
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
          .
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
            within 5 business days
          </li>
          <li>
            <strong className="font-medium opacity-80">Initial assessment:</strong>{" "}
            within 10 business days
          </li>
          <li>
            <strong className="font-medium opacity-80">Fix and disclosure:</strong>{" "}
            we aim to release a fix within 30 days of confirmation, followed by
            coordinated public disclosure
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
            <strong className="font-medium opacity-80">Sandboxed execution</strong> —
            coding agents run in isolated containers, separate from the host
            system
          </li>
          <li>
            <strong className="font-medium opacity-80">Minimal permissions</strong> —
            integrations request only the scopes necessary to function
          </li>
          <li>
            <strong className="font-medium opacity-80">Encryption in transit</strong> —
            all connections use TLS
          </li>
          <li>
            <strong className="font-medium opacity-80">Credential isolation</strong> —
            secrets and API keys are stored encrypted, never logged, and not
            exposed to agent processes
          </li>
          <li>
            <strong className="font-medium opacity-80">Open source</strong> —
            the entire codebase is public and auditable under the MIT License
          </li>
        </ul>
      </Section>

      <Section heading="Scope">
        <p>
          This policy covers the 143 open-source project and the hosted service
          at 143.dev. If you find a vulnerability in a third-party dependency,
          please report it to the respective maintainers and let us know so we
          can update.
        </p>
      </Section>

      <Section heading="Recognition">
        <p>
          We appreciate the work of security researchers. With your permission,
          we are happy to credit you in the security advisory and release notes
          when a vulnerability is fixed.
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
