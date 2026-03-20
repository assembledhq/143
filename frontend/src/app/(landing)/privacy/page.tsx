import Link from "next/link";
import LegalPageLayout, {
  Section,
} from "@/components/landing/legal-page-layout";

export default function PrivacyPage() {
  return (
    <LegalPageLayout title="Privacy Policy" lastUpdated="March 20, 2026">
      <Section heading="Scope">
        <p>
          This policy covers the 143 website and hosted service at 143.dev,
          operated by Assembled, Inc. It does not cover self-hosted deployments
          of the 143 open-source software except to explain that self-hosted
          operators, not Assembled, control their own infrastructure and
          data-handling practices.
        </p>
        <p>
          When you run 143 on your own infrastructure, data is not sent to
          Assembled by default. Self-hosted operators may still choose to send
          data to third-party services they configure, such as GitHub, Sentry,
          Linear, Slack, Anthropic, OpenAI, OpenRouter, or Google Gemini.
        </p>
      </Section>

      <Section heading="Who controls data">
        <p>
          We act as the provider of the hosted service for account, operational,
          and security data related to 143.dev. If you use 143.dev through an
          organization, that organization may control repositories, issues,
          tickets, prompts, and other workspace content made available to the
          service.
        </p>
      </Section>

      <Section heading="What we collect">
        <p>
          When you use the hosted service at 143.dev, we store code, prompts,
          agent output, and related workspace data on our servers in order to
          run cloud-hosted coding agents on your behalf. If you use the
          open-source, self-hosted version of 143, none of this data is sent to
          or stored by Assembled — it stays entirely on your own
          infrastructure.
        </p>
        <ul className="list-disc pl-5 space-y-1.5">
          <li>
            <strong className="font-medium opacity-80">Account information</strong> -
            name, email address, organization membership, and role
          </li>
          <li>
            <strong className="font-medium opacity-80">Authentication and session data</strong> -
            login provider identifiers, session records, security tokens, and
            short-lived OAuth state
          </li>
          <li>
            <strong className="font-medium opacity-80">Customer content</strong> -
            source code, issue descriptions, pull request content, review
            comments, stack traces, repository metadata, and other data made
            available through connected services
          </li>
          <li>
            <strong className="font-medium opacity-80">Agent and workspace data</strong> -
            prompts, manual-session messages, uploaded image URLs, generated
            diffs, logs, token-usage metadata, and temporary workspace snapshots
          </li>
          <li>
            <strong className="font-medium opacity-80">Technical and security data</strong> -
            IP address, user-agent, request identifiers, audit events, and
            device/browser details we observe when you use the service
          </li>
        </ul>
      </Section>

      <Section heading="Sources of personal data">
        <p>
          We collect data directly from you, from your organization, from your
          browser when you use 143.dev, and from connected services such as
          GitHub, Google, Sentry, Linear, and Slack.
        </p>
      </Section>

      <Section heading="How we use data">
        <ul className="list-disc pl-5 space-y-1.5">
          <li>To provide, secure, and maintain the hosted service</li>
          <li>To authenticate users, manage sessions, and enforce access controls</li>
          <li>To sync connected services and run coding-agent workflows</li>
          <li>To generate, validate, and present model or agent output</li>
          <li>To troubleshoot incidents, prevent abuse, and meet legal obligations</li>
          <li>To communicate with you about your account, support, or service changes</li>
        </ul>
      </Section>

      <Section heading="Legal bases">
        <p>
          Depending on your location, we rely on contractual necessity,
          legitimate interests, consent, and legal compliance to process
          personal data. Our legitimate interests include securing the service,
          preventing abuse, debugging failures, and improving reliability.
        </p>
      </Section>

      <Section heading="AI providers and data retention">
        <p>
          We may send prompts and related workspace content to AI providers that
          power the service, such as Anthropic, OpenAI, OpenRouter, or Google
          Gemini. Which provider receives content depends on the model and
          credentials configured for the run. If your organization supplies its
          own API keys, those requests run through your configured provider
          accounts.
        </p>
        <p>
          AI providers may temporarily retain request data in accordance with
          their own policies. For example, as of the date of this policy,
          Anthropic and OpenAI retain API inputs for up to 30 days for trust and
          safety purposes and do not use API data to train models by default.
          Google retains Gemini API data in accordance with its Cloud data
          processing terms. We encourage you to review each provider&apos;s
          data-handling policies directly, as they may change.
        </p>
      </Section>

      <Section heading="Cookies">
        <p>
          We use session, CSRF, and short-lived OAuth flow cookies to
          authenticate users, protect against request forgery, carry invitation
          state, and complete login or integration flows. We do not use
          third-party advertising cookies or cross-site tracking cookies on the
          hosted service.
        </p>
      </Section>

      <Section heading="Sharing and subprocessors">
        <p>
          We share data with service providers and infrastructure partners only
          as needed to operate 143.dev, including hosting, storage, source
          control, authentication, issue-tracking, collaboration, email, and AI
          inference providers. We do not sell personal information and we do not
          share personal information for cross-context behavioral advertising.
        </p>
        <p>
          A current list of our subprocessors is available upon request by
          contacting{" "}
          <a
            href="mailto:privacy@assembled.com"
            className="underline underline-offset-2"
          >
            privacy@assembled.com
          </a>
          . We will notify Organizations that have executed a Data Processing
          Addendum before adding new subprocessors that materially change how
          personal data is processed, providing reasonable time to object.
        </p>
      </Section>

      <Section heading="International transfers">
        <p>
          We and our service providers may process data in the United States and
          other countries where we or they operate. Data-protection laws in
          those locations may differ from the laws where you live.
        </p>
      </Section>

      <Section heading="Retention">
        <p>
          We retain data for different periods depending on the category and
          purpose:
        </p>
        <ul className="list-disc pl-5 space-y-1.5">
          <li>
            <strong className="font-medium opacity-80">Account information</strong>{" "}
            - retained while the account is active, plus 30 days after account
            deletion to allow for recovery or dispute resolution
          </li>
          <li>
            <strong className="font-medium opacity-80">Authentication and session data</strong>{" "}
            - session tokens expire according to platform defaults; login audit
            records are retained for up to 12 months
          </li>
          <li>
            <strong className="font-medium opacity-80">Agent and workspace data</strong>{" "}
            - session messages, generated diffs, and logs are retained while the
            workspace is active; temporary workspace snapshots are generally
            purged within 30 days after a run completes
          </li>
          <li>
            <strong className="font-medium opacity-80">Technical and security data</strong>{" "}
            - IP addresses, request identifiers, and audit events are retained
            for up to 12 months for security and compliance purposes
          </li>
          <li>
            <strong className="font-medium opacity-80">Backups</strong>{" "}
            - deleted data may persist in encrypted backups for up to 90 days
            after deletion before being purged
          </li>
        </ul>
        <p>
          Specific retention periods may vary based on product settings
          configured by your Organization, applicable legal obligations, or
          ongoing security investigations. Where required by law, we will
          retain data for the minimum period necessary to comply.
        </p>
      </Section>

      <Section heading="Your rights and choices">
        <p>
          Depending on your location, you may have rights to access, correct,
          delete, export, or object to certain processing of personal data. If a
          request relates to organization-controlled content, we may direct you
          to your organization administrator first.
        </p>
        <p>
          Residents of certain U.S. states have additional rights under
          applicable privacy laws, including the California Consumer Privacy Act
          (CCPA/CPRA), the Colorado Privacy Act, the Connecticut Data Privacy
          Act, the Delaware Personal Data Privacy Act, and similar state
          statutes. These rights may include the right to know what personal
          data we collect and how it is used, the right to request deletion, the
          right to opt out of the sale or sharing of personal data (we do not
          sell personal data), and the right to non-discrimination for
          exercising your rights. To make a request, contact us at{" "}
          <a
            href="mailto:privacy@assembled.com"
            className="underline underline-offset-2"
          >
            privacy@assembled.com
          </a>
          .
        </p>
      </Section>

      <Section heading="European and UK users">
        <p>
          If you are located in the European Economic Area (EEA), the United
          Kingdom, or Switzerland, you may have additional rights under the
          General Data Protection Regulation (GDPR) or the UK GDPR, including:
        </p>
        <ul className="list-disc pl-5 space-y-1.5">
          <li>
            <strong className="font-medium opacity-80">Right of access</strong>{" "}
            - to obtain confirmation of whether we process your personal data
            and to receive a copy
          </li>
          <li>
            <strong className="font-medium opacity-80">Right to rectification</strong>{" "}
            - to correct inaccurate or incomplete personal data
          </li>
          <li>
            <strong className="font-medium opacity-80">Right to erasure</strong>{" "}
            - to request deletion of your personal data, subject to legal
            retention requirements
          </li>
          <li>
            <strong className="font-medium opacity-80">Right to restriction</strong>{" "}
            - to request that we limit processing of your personal data in
            certain circumstances
          </li>
          <li>
            <strong className="font-medium opacity-80">Right to data portability</strong>{" "}
            - to receive your personal data in a structured, commonly used,
            machine-readable format and to transmit it to another controller
          </li>
          <li>
            <strong className="font-medium opacity-80">Right to object</strong>{" "}
            - to object to processing based on legitimate interests or for
            direct marketing purposes
          </li>
          <li>
            <strong className="font-medium opacity-80">Right to lodge a complaint</strong>{" "}
            - to file a complaint with your local data protection supervisory
            authority
          </li>
        </ul>
        <p>
          Where we process personal data on the basis of your consent, you have
          the right to withdraw that consent at any time without affecting the
          lawfulness of processing carried out before withdrawal. To exercise
          any of these rights, contact us at{" "}
          <a
            href="mailto:privacy@assembled.com"
            className="underline underline-offset-2"
          >
            privacy@assembled.com
          </a>
          . We will respond within the timeframes required by applicable law
          (generally within one month, with the possibility of extension for
          complex requests). Where we transfer personal data outside the EEA or
          UK, we rely on appropriate safeguards such as Standard Contractual
          Clauses approved by the European Commission.
        </p>
      </Section>

      <Section heading="Security">
        <p>
          We use administrative, technical, and organizational safeguards
          designed to protect data handled by the hosted service. For more
          detail, see our{" "}
          <Link href="/security" className="underline underline-offset-2">
            Security page
          </Link>
          .
        </p>
      </Section>

      <Section heading="Children">
        <p>
          143 is not directed to children under 13, and we do not knowingly
          collect personal information from children under 13.
        </p>
      </Section>

      <Section heading="Changes">
        <p>
          We may update this policy from time to time. If we make material
          changes, we will update the date at the top of this page and may
          provide additional notice where required.
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
          </a>
          .
        </p>
      </Section>
    </LegalPageLayout>
  );
}
