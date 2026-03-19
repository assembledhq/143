import Link from "next/link";
import LegalPageLayout, {
  Section,
} from "@/components/landing/legal-page-layout";

export default function TermsPage() {
  return (
    <LegalPageLayout title="Terms of Use" lastUpdated="March 18, 2026">
      <Section heading="Scope">
        <p>
          These terms govern your use of the 143 website and hosted service at
          143.dev, operated by Assembled, Inc. They do not govern the 143
          open-source software itself - that is licensed under the{" "}
          <a
            href="https://github.com/assembledhq/143/blob/main/LICENSE"
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2"
          >
            MIT License
          </a>{" "}
          and applies independently.
        </p>
        <p>
          By using 143.dev, you agree to these terms. If you do not agree, do
          not use the service.
        </p>
      </Section>

      <Section heading="The service">
        <p>
          143 is an open-source platform that uses AI agents to analyze
          production issues and prepare code changes, validations, and pull
          requests. The hosted service at 143.dev provides a managed version of
          that platform, including account management, integrations, and
          infrastructure.
        </p>
      </Section>

      <Section heading="Accounts">
        <p>
          You are responsible for maintaining the confidentiality of your
          account credentials, providing accurate account information, and using
          the service only as authorized by your organization and these terms.
          Organization administrators may control access to workspace content
          and settings for users in their organization.
        </p>
      </Section>

      <Section heading="Your content">
        <p>
          You retain ownership of your code, prompts, repositories, issues, and
          other content. You grant us a limited license to host, store,
          transmit, reproduce, and process that content as needed to operate,
          secure, and support the hosted service.
        </p>
      </Section>

      <Section heading="AI-generated output">
        <p>
          143 uses automated systems and third-party AI models to generate
          suggestions, diffs, summaries, and pull requests. AI-generated output
          may be incomplete, inaccurate, insecure, or unsuitable for your use
          case. You are responsible for reviewing, testing, approving, and
          deciding whether to use any output before relying on it in production.
        </p>
      </Section>

      <Section heading="Third-party models and services">
        <p>
          The service relies on third-party AI model providers, including
          Anthropic, OpenAI, OpenRouter, and Google, to process prompts and
          generate output. Your use of the service is also subject to the terms
          and policies of the providers whose models are used for your requests.
          If your organization supplies its own API keys, the provider&apos;s
          terms apply directly to that usage.
        </p>
      </Section>

      <Section heading="Acceptable use">
        <p>You agree not to:</p>
        <ul className="list-disc pl-5 space-y-1.5">
          <li>Use the service to violate any applicable law or regulation</li>
          <li>Interfere with or disrupt the service or its infrastructure</li>
          <li>
            Attempt to gain unauthorized access to other users&apos; accounts or
            data
          </li>
          <li>Use the service to transmit malware or malicious code</li>
          <li>
            Scrape, crawl, or index the service in a way that places undue
            burden on our infrastructure
          </li>
          <li>
            Use the service to exfiltrate secrets, access data without
            authorization, or interfere with another user, organization, or
            connected service
          </li>
        </ul>
      </Section>

      <Section heading="Open-source contributions">
        <p>
          Contributions to the open-source repository are governed by the{" "}
          <a
            href="https://github.com/assembledhq/143/blob/main/LICENSE"
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2"
          >
            MIT License
          </a>{" "}
          and the repository&apos;s{" "}
          <a
            href="https://github.com/assembledhq/143/blob/main/CONTRIBUTING.md"
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2"
          >
            CONTRIBUTING.md
          </a>
          . Our contribution model is inbound=outbound: unless explicitly stated
          otherwise, contributions intentionally submitted for inclusion in the
          project are licensed under the same MIT License.
        </p>
      </Section>

      <Section heading="Service availability">
        <p>
          We aim to keep 143.dev available and reliable, but we do not guarantee
          uninterrupted access. The service is provided on an &quot;as is&quot;
          and &quot;as available&quot; basis. We may modify, suspend, or
          discontinue the service at any time.
        </p>
      </Section>

      <Section heading="Suspension and termination">
        <p>
          You may stop using the service at any time. We may suspend or
          terminate access if you violate these terms, create security risk, or
          misuse the service. Termination of the hosted service does not affect
          your rights under the MIT License to use the open-source software on a
          self-hosted basis.
        </p>
      </Section>

      <Section heading="Disclaimer of warranties">
        <p>
          To the maximum extent permitted by law, the service is provided
          without warranties of any kind, whether express, implied, or
          statutory, including implied warranties of merchantability, fitness for
          a particular purpose, and non-infringement. This is consistent with
          the MIT License under which the underlying software is distributed.
        </p>
      </Section>

      <Section heading="Limitation of liability">
        <p>
          To the maximum extent permitted by law, Assembled, Inc. shall not be
          liable for any indirect, incidental, special, consequential, or
          punitive damages, or any loss of profits, data, or goodwill, arising
          out of or related to your use of the service. To the maximum extent
          permitted by law, our aggregate liability for all claims arising out
          of or related to the service will not exceed the greater of $100 USD
          or the amount you paid us for the service in the 12 months before the
          event giving rise to the claim.
        </p>
      </Section>

      <Section heading="Changes">
        <p>
          We may update these terms from time to time. If we make material
          changes, we will notify you by updating the date at the top of this
          page. Continued use of the service after changes constitutes
          acceptance.
        </p>
      </Section>

      <Section heading="Dispute resolution">
        <p>
          Any dispute arising from these terms or your use of the service will
          be resolved by binding individual arbitration administered under the
          rules of the American Arbitration Association in San Francisco,
          California. You agree to waive any right to participate in a class
          action, class-wide arbitration, or representative proceeding. Either
          party may seek injunctive relief in a court of competent jurisdiction
          to protect intellectual property rights.
        </p>
      </Section>

      <Section heading="Governing law">
        <p>
          These terms are governed by the laws of the State of California,
          without regard to conflict of law principles.
        </p>
      </Section>

      <Section heading="Contact">
        <p>
          Questions about these terms? Reach us at{" "}
          <a
            href="mailto:legal@assembled.com"
            className="underline underline-offset-2"
          >
            legal@assembled.com
          </a>{" "}
          and see our{" "}
          <Link href="/privacy" className="underline underline-offset-2">
            Privacy Policy
          </Link>{" "}
          for data-handling terms.
        </p>
      </Section>
    </LegalPageLayout>
  );
}
