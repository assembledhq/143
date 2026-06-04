import Link from "next/link";
import { landingTypography as type } from "./landing-typography";

interface FooterProps {
  isDark: boolean;
}

export default function Footer({ isDark }: FooterProps) {
  return (
    <footer
      className="px-6 sm:px-10 py-12 sm:py-16"
      style={{ background: isDark ? "#08080f" : "#eef4f8" }}
    >
      <div className="max-w-5xl mx-auto">
        <div className="flex flex-col sm:flex-row justify-between gap-10">
          {/* Brand */}
          <div className="space-y-3">
            <p
              className={`${type.navBrand} ${isDark ? "text-white/80" : "text-slate-800"}`}
            >
              143
            </p>
            <p
              className={`${type.footerLink} leading-relaxed max-w-xs ${isDark ? "text-white/30" : "text-slate-500"}`}
            >
              Open-source coding-agent infrastructure for teams.
              <br />
              Built by{" "}
              <a
                href="https://www.assembled.com"
                target="_blank"
                rel="noopener noreferrer"
                className={`underline underline-offset-2 ${isDark ? "hover:text-white/50" : "hover:text-slate-700"}`}
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
                className={`${type.footerLink} font-medium uppercase tracking-wider ${isDark ? "text-white/40" : "text-slate-500"}`}
              >
                Project
              </p>
              <ul className="space-y-2">
                <li>
                  <Link
                    href="/about"
                    className={`${type.footerLink} ${isDark ? "text-white/30 hover:text-white/60" : "text-slate-500 hover:text-slate-700"} transition-colors`}
                  >
                    About
                  </Link>
                </li>
                <li>
                  <Link
                    href="/docs"
                    className={`${type.footerLink} ${isDark ? "text-white/30 hover:text-white/60" : "text-slate-500 hover:text-slate-700"} transition-colors`}
                  >
                    Docs
                  </Link>
                </li>
                <li>
                  <a
                    href="https://github.com/assembledhq/143"
                    target="_blank"
                    rel="noopener noreferrer"
                    className={`${type.footerLink} ${isDark ? "text-white/30 hover:text-white/60" : "text-slate-500 hover:text-slate-700"} transition-colors`}
                  >
                    GitHub
                  </a>
                </li>
                <li>
                  <a
                    href="https://github.com/assembledhq/143/blob/main/LICENSE"
                    target="_blank"
                    rel="noopener noreferrer"
                    className={`${type.footerLink} ${isDark ? "text-white/30 hover:text-white/60" : "text-slate-500 hover:text-slate-700"} transition-colors`}
                  >
                    MIT License
                  </a>
                </li>
              </ul>
            </div>

            <div className="space-y-3">
              <p
                className={`${type.footerLink} font-medium uppercase tracking-wider ${isDark ? "text-white/40" : "text-slate-500"}`}
              >
                Legal
              </p>
              <ul className="space-y-2">
                <li>
                  <Link
                    href="/privacy"
                    className={`${type.footerLink} ${isDark ? "text-white/30 hover:text-white/60" : "text-slate-500 hover:text-slate-700"} transition-colors`}
                  >
                    Privacy
                  </Link>
                </li>
                <li>
                  <Link
                    href="/terms"
                    className={`${type.footerLink} ${isDark ? "text-white/30 hover:text-white/60" : "text-slate-500 hover:text-slate-700"} transition-colors`}
                  >
                    Terms
                  </Link>
                </li>
                <li>
                  <Link
                    href="/security"
                    className={`${type.footerLink} ${isDark ? "text-white/30 hover:text-white/60" : "text-slate-500 hover:text-slate-700"} transition-colors`}
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
          className={`mt-12 pt-6 border-t ${isDark ? "border-white/5" : "border-slate-300/50"}`}
        >
          <p
            className={`${type.footerLink} ${isDark ? "text-white/20" : "text-slate-400"}`}
          >
            &copy; {new Date().getFullYear()} Assembled, Inc.
          </p>
        </div>
      </div>
    </footer>
  );
}
