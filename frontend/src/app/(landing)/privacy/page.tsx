import Link from "next/link";
import LegalPageLayout, {
  Section,
} from "@/components/landing/legal-page-layout";

export default function PrivacyPage() {
  return (
    <LegalPageLayout title="Privacy Policy" lastUpdated="March 15, 2026">
      <Section heading="Scope">
        <p>
          This policy covers your use of the 143 website and hosted service at
          143.dev, operated by Assembled, Inc. It does not cover self-hosted
          deployments of the 143 open-source software — when you run 143 on your
          own infrastructure, no data is sent to Assembled.
        </p>
        <p>
          Because 143 is open source under the MIT License, you can verify this
          by inspecting the source code at any time.
        </p>
      </Section>

      <Section heading="What we collect">
        <p>When you use the 143.dev website and service, we may collect:</p>
        <ul className="list-disc pl-5 space-y-1.5">
          <li>
            <strong className="font-medium opacity-80">Account information</strong> — email
            address and name when you sign up
          </li>
          <li>
            <strong className="font-medium opacity-80">Authentication data</strong> — OAuth
            tokens from GitHub or other identity providers you connect
          </li>
          <li>
            <strong className="font-medium opacity-80">Usage data</strong> — pages visited,
            features used, and general interaction patterns
          </li>
          <li>
            <strong className="font-medium opacity-80">Technical data</strong> — browser type,
            operating system, and IP address
          </li>
          <li>
            <strong className="font-medium opacity-80">Repository metadata</strong> — repository
            names, issue titles, and PR data you connect to the service
          </li>
        </ul>
      </Section>

      <Section heading="How we use your data">
        <ul className="list-disc pl-5 space-y-1.5">
          <li>To provide and operate the 143 service</li>
          <li>To authenticate you and manage your session</li>
          <li>To improve the service based on usage patterns</li>
          <li>To communicate with you about your account</li>
        </ul>
        <p>We do not sell your personal data to third parties.</p>
      </Section>

      <Section heading="Cookies">
        <p>
          We use a <code className="text-xs opacity-70">session_token</code>{" "}
          cookie for authentication. This is a strictly necessary cookie — the
          service does not function without it. We do not use third-party
          advertising or tracking cookies.
        </p>
      </Section>

      <Section heading="Third-party services">
        <p>
          We may use third-party services to help operate 143.dev, such as
          hosting providers, analytics tools, and payment processors. These
          services only receive the data necessary to perform their function and
          are bound by their own privacy policies.
        </p>
      </Section>

      <Section heading="Open-source contributions">
        <p>
          If you contribute to 143 via GitHub (issues, pull requests, comments),
          that information is public and governed by GitHub&apos;s privacy
          policy. Contributions to the open-source project are retained
          indefinitely to maintain project integrity.
        </p>
      </Section>

      <Section heading="Data retention and deletion">
        <p>
          We retain your data for as long as your account is active. You can
          request deletion of your account and associated data by contacting us.
          Some data may be retained in backups for a limited period after
          deletion.
        </p>
      </Section>

      <Section heading="Your rights">
        <p>
          Depending on your location, you may have the right to access, correct,
          delete, or export your personal data. California residents have
          additional rights under the CCPA, including the right to know what
          personal information is collected and the right to opt out of the sale
          of personal information (we do not sell personal information).
        </p>
      </Section>

      <Section heading="Security">
        <p>
          We use industry-standard measures to protect your data, including
          encryption in transit (TLS) and at rest. For more details, see
          our{" "}
          <Link href="/security" className="underline underline-offset-2">
            Security page
          </Link>
          .
        </p>
      </Section>

      <Section heading="Children">
        <p>
          143 is not directed at children under 13. We do not knowingly collect
          personal information from children under 13.
        </p>
      </Section>

      <Section heading="Changes">
        <p>
          We may update this policy from time to time. If we make material
          changes, we will notify you by updating the date at the top of this
          page.
        </p>
      </Section>

      <Section heading="Contact">
        <p>
          For privacy questions, reach us at{" "}
          <a
            href="mailto:privacy@assembled.com"
            className="underline underline-offset-2"
          >
            privacy@assembled.com
          </a>{" "}
          or open an issue on{" "}
          <a
            href="https://github.com/assembledhq/143"
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2"
          >
            GitHub
          </a>
          .
        </p>
      </Section>
    </LegalPageLayout>
  );
}
