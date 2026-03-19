import Link from "next/link";
import LegalPageLayout, {
  Section,
} from "@/components/landing/legal-page-layout";

export default function PrivacyPage() {
  return (
    <LegalPageLayout title="Privacy Policy" lastUpdated="March 18, 2026">
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

      <Section heading="AI providers">
        <p>
          We may send prompts and related workspace content to AI providers that
          power the service, such as Anthropic, OpenAI, OpenRouter, or Google
          Gemini. Which provider receives content depends on the model and
          credentials configured for the run. If your organization supplies its
          own API keys, those requests run through your configured provider
          accounts.
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
          We retain data for different periods depending on the category and why
          we need it. Account records are generally kept while the account is
          active. Session messages, logs, and snapshots may be retained while a
          workspace is active, for debugging, or for security review, and then
          deleted according to product settings, retention jobs, backups, and
          legal obligations. Some information may persist in backups for a
          limited period after deletion.
        </p>
      </Section>

      <Section heading="Your rights and choices">
        <p>
          Depending on your location, you may have rights to access, correct,
          delete, export, or object to certain processing of personal data.
          California residents may also have rights under the CCPA/CPRA. If a
          request relates to organization-controlled content, we may direct you
          to your organization administrator first.
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
