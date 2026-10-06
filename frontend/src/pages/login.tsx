import * as React from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { LoginBranding } from "@/components/auth/LoginBranding";
import { LoginForm } from "@/components/auth/LoginForm";
import { Symbol } from "@/components/symbol";
import { useAuth } from "@/context/auth-context";

const footerLinks = [
  { icon: "headset_mic", label: "SSO Helpdesk" },
  { icon: "vpn_key", label: "Request Access" },
  { icon: "query_stats", label: "Status Page" },
] as const;

export default function LoginPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const { user, isLoading } = useAuth();

  const redirectAfterAuth = React.useCallback(() => {
    const from = (location.state as { from?: string } | null)?.from;
    if (!user) {
      const target = from && from !== "/login" ? from : "/dashboard";
      navigate(target, { replace: true });
    }
  }, [location.state, navigate, user]);

  // If already authenticated, redirect immediately
  React.useEffect(() => {
    if (user) {
      redirectAfterAuth();
    }
  }, [user, redirectAfterAuth]);

  return (
    <main className="flex min-h-screen w-full flex-col bg-surface pt-safe pb-safe overflow-x-hidden">
      {!isLoading && user ? (
        <span role="status" className="sr-only">
          Redirecting to dashboard...
        </span>
      ) : null}

      <div className="flex min-h-screen w-full flex-col lg:flex-row">
        {/* Left: Branding panel (desktop) */}
        <LoginBranding />

        {/* Right: Auth column */}
        <section className="flex flex-1 items-center justify-center px-margin py-space-xl">
          <div className="w-full max-w-md">
            {/* Compact brand (mobile / tablet) */}
            <div className="mb-5 flex items-center gap-2.5 lg:hidden">
              <span className="flex h-10 w-10 items-center justify-center rounded-lg bg-surface-container-low p-1.5 shadow-sm">
                <img
                  src="/brand/edp-mark.svg"
                  alt=""
                  className="h-full w-full object-contain"
                />
              </span>
              <span className="flex flex-col">
                <span className="font-headline-sm font-semibold tracking-tight text-on-surface">
                  EDP Platform
                </span>
                <span className="font-label-xs uppercase text-on-surface-variant">
                  Enterprise Data Platform
                </span>
              </span>
            </div>

            {/* Status row */}
            <div className="mb-4 flex flex-wrap items-center gap-2">
              <span className="inline-flex items-center gap-1.5 rounded-full bg-surface-container-low px-2.5 py-1 shadow-sm">
                <span className="h-1.5 w-1.5 rounded-full bg-tertiary-container animate-pulse" />
                <span className="font-label-xs uppercase tracking-wide text-tertiary">
                  All Systems Operational
                </span>
              </span>
              <span className="inline-flex items-center gap-1.5 rounded-full bg-surface-container px-2.5 py-1">
                <Symbol
                  name="verified_user"
                  size={13}
                  filled
                  className="text-primary"
                />
                <span className="font-label-xs uppercase tracking-wide text-on-surface-variant">
                  SOC2 Type II
                </span>
              </span>
            </div>

            <h1 className="font-headline-lg text-on-surface">
              Sign in to your workspace
            </h1>
            <p className="mt-1 font-body-md text-on-surface-variant">
              Use your corporate identity or SSO to reach your data mesh,
              lakehouse storage and pipelines.
            </p>

            <div className="mt-6">
              <LoginForm />
            </div>

            {/* Footer */}
            <footer className="mt-6 flex flex-col items-center gap-3">
              <nav className="flex flex-wrap items-center justify-center gap-x-4 gap-y-2">
                {footerLinks.map((link, index) => (
                  <React.Fragment key={link.label}>
                    {index > 0 ? (
                      <span className="hidden text-outline-variant text-[10px] sm:inline">
                        •
                      </span>
                    ) : null}
                    <a
                      href="#"
                      className="inline-flex items-center gap-1 font-label-xs text-secondary transition-colors hover:text-primary"
                      onClick={(event) => event.preventDefault()}
                    >
                      <Symbol name={link.icon} size={14} />
                      <span>{link.label}</span>
                    </a>
                  </React.Fragment>
                ))}
              </nav>
              <div className="text-center font-label-xs text-outline">
                <p>© 2026 Enterprise Data Platform Inc. All rights reserved.</p>
                <p>Encrypted via TLS 1.3 • AES-256 GCM</p>
              </div>
            </footer>
          </div>
        </section>
      </div>
    </main>
  );
}
