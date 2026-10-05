import * as React from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { toast } from "sonner";

import { LoginBranding } from "@/components/auth/LoginBranding";
import { LoginForm } from "@/components/auth/LoginForm";
import { useAuth } from "@/context/auth-context";

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
    <main
      className="flex min-h-screen w-full flex-col bg-surface pt-safe pb-safe overflow-x-hidden"
    >
      {!isLoading && user ? (
        <span role="status" className="sr-only">
          Redirecting to dashboard...
        </span>
      ) : null}

      <div className="flex w-full flex-col lg:flex-row gap-6 px-margin py-space-lg">
        {/* Left: Branding panel */}
        <LoginBranding className="w-full lg:w-auto flex-shrink-0" />

        {/* Right: Login form */}
        <div className="flex-1 flex flex-col lg:pr-8">
          <LoginForm />

          <div className="mt-4 text-center">
            <a
              href="#"
              className="font-label-xs text-primary transition-colors hover:text-primary-container"
              onClick={() => {
                // Trigger password reset flow
                toast("info", {
                  description: "Password reset initiation not yet configured.",
                  icon: <Symbol name="info" size={18} className="text-tertiary-fixed-dim" />,
                });
              }}
            >
              Forgot password?
            </a>
          </div>
        </div>
      </div>
    </main>
  );
}