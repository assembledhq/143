import Link from "next/link";
import { landingLayout as layout } from "./landing-layout";
import { landingTypography as type } from "./landing-typography";

interface FooterProps {
  isDark: boolean;
}

export default function Footer({ isDark }: FooterProps) {
  return (
    <footer
      className={`border-t px-6 py-12 sm:px-10 sm:py-16 ${
        isDark ? "border-white/10" : "border-[#e1ded5]"
      }`}
      style={{ background: isDark ? "#11110f" : "#efeee8" }}
    >
      <div className={layout.pageShell}>
        <div className="flex flex-col sm:flex-row justify-between gap-10">
          {/* Brand */}
          <div className="space-y-3">
            <p
              className={`${type.navBrand} ${isDark ? "text-[#f4f3ee]" : "text-[#1b1b19]"}`}
            >
              143
            </p>
            <p
              className={`${type.footerLink} leading-relaxed max-w-xs ${isDark ? "text-[#aaa89f]" : "text-[#6b6b65]"}`}
            >
              Open-source coding-agent infrastructure for teams.
              <br />
              Built by{" "}
              <a
                href="https://www.assembled.com"
                target="_blank"
                rel="noopener noreferrer"
                className={`underline decoration-current/40 underline-offset-4 ${isDark ? "hover:text-[#f4f3ee]" : "hover:text-[#1b1b19]"}`}
              >
                Assembled
              </a>
              .
            </p>
          </div>

          {/* Links */}
          <div className="flex gap-16">
            <div className="space-y-3">
              <p
                className={`${type.footerLink} font-medium uppercase tracking-wider ${isDark ? "text-[#7992ff]" : "text-[#315ce8]"}`}
              >
                Project
              </p>
              <ul className="space-y-2">
                <li>
                  <Link
                    href="/about"
                    className={`${type.footerLink} ${isDark ? "text-[#aaa89f] hover:text-[#f4f3ee]" : "text-[#6b6b65] hover:text-[#1b1b19]"} transition-colors`}
                  >
                    About
                  </Link>
                </li>
                <li>
                  <Link
                    href="/why-143"
                    className={`${type.footerLink} ${isDark ? "text-[#aaa89f] hover:text-[#f4f3ee]" : "text-[#6b6b65] hover:text-[#1b1b19]"} transition-colors`}
                  >
                    Why &ldquo;143&rdquo;?
                  </Link>
                </li>
                <li>
                  <Link
                    href="/docs"
                    className={`${type.footerLink} ${isDark ? "text-[#aaa89f] hover:text-[#f4f3ee]" : "text-[#6b6b65] hover:text-[#1b1b19]"} transition-colors`}
                  >
                    Docs
                  </Link>
                </li>
                <li>
                  <a
                    href="https://github.com/assembledhq/143"
                    target="_blank"
                    rel="noopener noreferrer"
                    className={`${type.footerLink} ${isDark ? "text-[#aaa89f] hover:text-[#f4f3ee]" : "text-[#6b6b65] hover:text-[#1b1b19]"} transition-colors`}
                  >
                    GitHub
                  </a>
                </li>
                <li>
                  <a
                    href="https://github.com/assembledhq/143/blob/main/LICENSE"
                    target="_blank"
                    rel="noopener noreferrer"
                    className={`${type.footerLink} ${isDark ? "text-[#aaa89f] hover:text-[#f4f3ee]" : "text-[#6b6b65] hover:text-[#1b1b19]"} transition-colors`}
                  >
                    MIT License
                  </a>
                </li>
              </ul>
            </div>

            <div className="space-y-3">
              <p
                className={`${type.footerLink} font-medium uppercase tracking-wider ${isDark ? "text-[#7992ff]" : "text-[#315ce8]"}`}
              >
                Legal
              </p>
              <ul className="space-y-2">
                <li>
                  <Link
                    href="/privacy"
                    className={`${type.footerLink} ${isDark ? "text-[#aaa89f] hover:text-[#f4f3ee]" : "text-[#6b6b65] hover:text-[#1b1b19]"} transition-colors`}
                  >
                    Privacy
                  </Link>
                </li>
                <li>
                  <Link
                    href="/terms"
                    className={`${type.footerLink} ${isDark ? "text-[#aaa89f] hover:text-[#f4f3ee]" : "text-[#6b6b65] hover:text-[#1b1b19]"} transition-colors`}
                  >
                    Terms
                  </Link>
                </li>
                <li>
                  <Link
                    href="/security"
                    className={`${type.footerLink} ${isDark ? "text-[#aaa89f] hover:text-[#f4f3ee]" : "text-[#6b6b65] hover:text-[#1b1b19]"} transition-colors`}
                  >
                    Security
                  </Link>
                </li>
              </ul>
            </div>
          </div>
        </div>

        {/* Bottom */}
        <div
          className={`mt-12 border-t pt-6 ${isDark ? "border-white/10" : "border-[#dad7ce]"}`}
        >
          <p
            className={`${type.footerLink} ${isDark ? "text-[#aaa89f]/65" : "text-[#6b6b65]/75"}`}
          >
            &copy; {new Date().getFullYear()} Assembled, Inc.
          </p>
        </div>
      </div>
    </footer>
  );
}
