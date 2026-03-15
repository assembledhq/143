import Link from "next/link";
import LegalPageLayout, {
  Section,
} from "@/components/landing/legal-page-layout";

export default function TermsPage() {
  return (
    <LegalPageLayout title="Terms of Use" lastUpdated="March 15, 2026">
      <Section heading="Scope">
        <p>
          These terms govern your use of the 143 website and hosted service at
          143.dev, operated by Assembled, Inc. They do not govern the 143
          open-source software itself — that is licensed under the{" "}
          <a
            href="https://github.com/assembledhq/143/blob/main/LICENSE"
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2"
          >
            MIT License
          </a>{" "}
          and its terms apply independently.
        </p>
        <p>
          By using 143.dev, you agree to these terms. If you do not agree, do
          not use the service.
        </p>
      </Section>

      <Section heading="The service">
        <p>
          143 is an open-source platform that uses AI agents to analyze
          production issues and submit validated pull requests. The hosted
          service at 143.dev provides a managed version of this platform,
          including account management, integrations, and infrastructure.
        </p>
      </Section>

      <Section heading="Accounts">
        <p>
          You are responsible for maintaining the security of your account
          credentials. You must provide accurate information when creating an
          account. You are responsible for all activity that occurs under your
          account.
        </p>
      </Section>

      <Section heading="Acceptable use">
        <p>You agree not to:</p>
        <ul className="list-disc pl-5 space-y-1.5">
          <li>
            Use the service to violate any applicable law or regulation
          </li>
          <li>
            Interfere with or disrupt the service or its infrastructure
          </li>
          <li>
            Attempt to gain unauthorized access to other users&apos; accounts or
            data
          </li>
          <li>
            Use the service to transmit malware or malicious code
          </li>
          <li>
            Scrape, crawl, or index the service in a way that places undue
            burden on our infrastructure
          </li>
        </ul>
      </Section>

      <Section heading="Your data">
        <p>
          You retain ownership of all code, issues, and other content you
          connect to or create through the service. We do not claim intellectual
          property rights over your content. See our{" "}
          <Link href="/privacy" className="underline underline-offset-2">
            Privacy Policy
          </Link>{" "}
          for how we handle your data.
        </p>
      </Section>

      <Section heading="Open-source contributions">
        <p>
          Contributions to the 143 open-source project (pull requests, issues,
          code) are governed by the{" "}
          <a
            href="https://github.com/assembledhq/143/blob/main/LICENSE"
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2"
          >
            MIT License
          </a>
          , not these terms. By contributing, you agree that your contributions
          are licensed under the same MIT License.
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
          out of or related to your use of the service.
        </p>
      </Section>

      <Section heading="Termination">
        <p>
          You may stop using the service at any time. We may suspend or
          terminate your access if you violate these terms. Upon termination,
          your right to use the hosted service ends, but the open-source
          software remains available under the MIT License — you can always
          self-host.
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
